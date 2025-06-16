package machineset

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	openshiftfeatures "github.com/openshift/api/features"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/machine-api-operator/pkg/controller/machine"
	mapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/openshift/machine-api-operator/pkg/util"
	"github.com/openshift/machine-api-operator/pkg/util/conditions"
	utils "github.com/openshift/machine-api-provider-aws/pkg/actuators/machine"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
	// This exposes compute information based on the providerSpec input.
	// This is needed by the autoscaler to foresee upcoming capacity when scaling from zero.
	// https://github.com/openshift/enhancements/pull/186
	cpuKey    = "machine.openshift.io/vCPU"
	memoryKey = "machine.openshift.io/memoryMb"
	gpuKey    = "machine.openshift.io/GPU"
	labelsKey = "capacity.cluster-autoscaler.kubernetes.io/labels"
)

// Reconciler reconciles machineSets.
type Reconciler struct {
	Client              client.Client
	Log                 logr.Logger
	AwsClientBuilder    awsclient.AwsClientBuilderFuncType
	RegionCache         awsclient.RegionCache
	ConfigManagedClient client.Client
	InstanceTypesCache  InstanceTypesCache
	Gate                featuregate.MutableFeatureGate

	recorder record.EventRecorder
	scheme   *runtime.Scheme
}

// SetupWithManager creates a new controller for a manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&machinev1beta1.MachineSet{}).
		WithOptions(options).
		Build(r)

	if err != nil {
		return fmt.Errorf("failed setting up with a controller manager: %w", err)
	}

	r.recorder = mgr.GetEventRecorderFor("machineset-controller")
	r.scheme = mgr.GetScheme()
	return nil
}

// Reconcile implements controller runtime Reconciler interface.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("machineset", req.Name, "namespace", req.Namespace)
	logger.V(3).Info("Reconciling")

	machineSet := &machinev1beta1.MachineSet{}
	if err := r.Client.Get(ctx, req.NamespacedName, machineSet); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, return. Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	// Ignore deleted MachineSets, this can happen when foregroundDeletion
	// is enabled
	if !machineSet.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if r.Gate.Enabled(featuregate.Feature(openshiftfeatures.FeatureGateMachineAPIMigration)) {
		// The paused condition is set by the machine-api-operator's machineset controller
		// here: https://github.com/openshift/machine-api-operator/blob/ea46700bc132f1ba40d91f523f4b3a16dfafe8b8/pkg/controller/machineset/controller.go#L176-L231,
		// once it observes the machineset .status.authoritativeAPI is not MachineAPI.
		// This is a minor controller meant for annotations, so starts skipping further action
		// only when the main controller sets the paused condition.
		// Context for this decision here: https://redhat-internal.slack.com/archives/GE2HQ9QP4/p1747830982876869
		if conditions.IsTrue(machineSet, machine.PausedCondition) {
			klog.V(3).Infof("%v: machine set has paused condition, taking no further action", machineSet.Name)
			return ctrl.Result{}, nil
		}

		// Reconciliation can continue only if the authoritativeAPI is set to a value is set to MachineAPI.
		// This extra check is needed as in early stages of reconcilation of a new MAPI MachineSet the Paused condition might not yet be set,
		// as such the authority status needs to be checked directly to ensure correct behaviour.
		if machineSet.Status.AuthoritativeAPI != machinev1beta1.MachineAuthorityMachineAPI {
			klog.V(3).Infof("%v: machine set has .status.authoritativeAPI set to %q, taking no further action", machineSet.Name, machineSet.Status.AuthoritativeAPI)
			return ctrl.Result{}, nil
		}
	}

	originalMachineSetToPatch := client.MergeFrom(machineSet.DeepCopy())

	result, err := r.reconcile(machineSet)
	if err != nil {
		logger.Error(err, "Failed to reconcile MachineSet")
		r.recorder.Eventf(machineSet, corev1.EventTypeWarning, "ReconcileError", "%v", err)
		// we don't return here so we want to attempt to patch the machine regardless of an error.
	}

	if err := r.Client.Patch(ctx, machineSet, originalMachineSetToPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch machineSet: %v", err)
	}

	if isInvalidConfigurationError(err) {
		// For situations where requeuing won't help we don't return error.
		// https://github.com/kubernetes-sigs/controller-runtime/issues/617
		return result, nil
	}
	return result, err
}

func isInvalidConfigurationError(err error) bool {
	switch t := err.(type) {
	case *mapierrors.MachineError:
		if t.Reason == machinev1beta1.InvalidConfigurationMachineError {
			return true
		}
	}
	return false
}

func (r *Reconciler) reconcile(machineSet *machinev1beta1.MachineSet) (ctrl.Result, error) {
	klog.V(3).Infof("%v: Reconciling MachineSet", machineSet.Name)
	providerConfig, err := utils.ProviderSpecFromRawExtension(machineSet.Spec.Template.Spec.ProviderSpec.Value)
	if err != nil {
		return ctrl.Result{}, mapierrors.InvalidMachineConfiguration("failed to get providerConfig: %v", err)
	}

	if providerConfig.CredentialsSecret == nil {
		return ctrl.Result{}, mapierrors.InvalidMachineConfiguration("nil credentialsSecret for machineSet %s", machineSet.Name)
	}

	awsClient, err := r.AwsClientBuilder(r.Client, providerConfig.CredentialsSecret.Name, machineSet.Namespace, providerConfig.Placement.Region, r.ConfigManagedClient, r.RegionCache)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error creating aws client: %w", err)
	}

	instanceType, err := r.InstanceTypesCache.GetInstanceType(awsClient, providerConfig.Placement.Region, providerConfig.InstanceType)
	if err != nil {
		klog.Errorf("Unable to set scale from zero annotations: unknown instance type %s: %v", providerConfig.InstanceType, err)
		klog.Errorf("Autoscaling from zero will not work. To fix this, manually populate machine annotations for your instance type: %v", []string{cpuKey, memoryKey, gpuKey})

		// Returning no error to prevent further reconciliation, as user intervention is now required but emit an informational event
		r.recorder.Eventf(machineSet, corev1.EventTypeWarning, "FailedUpdate", "Failed to set autoscaling from zero annotations, instance type unknown")
		return ctrl.Result{}, nil
	}

	if machineSet.Annotations == nil {
		machineSet.Annotations = make(map[string]string)
	}

	// TODO: get annotations keys from machine API
	machineSet.Annotations[cpuKey] = strconv.FormatInt(instanceType.VCPU, 10)
	machineSet.Annotations[memoryKey] = strconv.FormatInt(instanceType.MemoryMb, 10)
	machineSet.Annotations[gpuKey] = strconv.FormatInt(instanceType.GPU, 10)
	// We guarantee that any existing labels provided via the capacity annotations are preserved.
	// See https://github.com/kubernetes/autoscaler/pull/5382 and https://github.com/kubernetes/autoscaler/pull/5697
	machineSet.Annotations[labelsKey] = util.MergeCommaSeparatedKeyValuePairs(
		fmt.Sprintf("kubernetes.io/arch=%s", instanceType.CPUArchitecture),
		machineSet.Annotations[labelsKey])
	return ctrl.Result{}, nil
}
