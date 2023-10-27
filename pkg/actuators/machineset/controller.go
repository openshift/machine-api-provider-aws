package machineset

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/openshift/machine-api-operator/pkg/util"
	annotationsutil "github.com/openshift/machine-api-operator/pkg/util/machineset"
	utils "github.com/openshift/machine-api-provider-aws/pkg/actuators/machine"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
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
		klog.Errorf("Autoscaling from zero will not work. To fix this, manually populate machine annotations for your instance type: %v", []string{annotationsutil.CpuKey, annotationsutil.MemoryKey, annotationsutil.GpuCountKey})

		// Returning no error to prevent further reconciliation, as user intervention is now required but emit an informational event
		r.recorder.Eventf(machineSet, corev1.EventTypeWarning, "FailedUpdate", "Failed to set autoscaling from zero annotations, instance type unknown")
		return ctrl.Result{}, nil
	}

	if machineSet.Annotations == nil {
		machineSet.Annotations = make(map[string]string)
	}

	machineSet.Annotations = annotationsutil.SetCpuAnnotation(machineSet.Annotations, strconv.FormatInt(instanceType.VCPU, 10))
	machineSet.Annotations = annotationsutil.SetMemoryAnnotation(machineSet.Annotations, strconv.FormatInt(instanceType.MemoryMb, 10))
	machineSet.Annotations = annotationsutil.SetGpuCountAnnotation(machineSet.Annotations, strconv.FormatInt(instanceType.GPU, 10))
	// TODO: We currently only support nvidia as GPU type. Once proper GPU types are introduced, we
	// can pass the value in the second argument of the function SetGpuTypeAnnotation.
	machineSet.Annotations = annotationsutil.SetGpuTypeAnnotation(machineSet.Annotations, annotationsutil.GpuNvidiaType)
	// We guarantee that any existing labels provided via the capacity annotations are preserved.
	// See https://github.com/kubernetes/autoscaler/pull/5382 and https://github.com/kubernetes/autoscaler/pull/5697
	machineSet.Annotations[labelsKey] = util.MergeCommaSeparatedKeyValuePairs(
		fmt.Sprintf("kubernetes.io/arch=%s", instanceType.CPUArchitecture),
		machineSet.Annotations[labelsKey])
	return ctrl.Result{}, nil
}
