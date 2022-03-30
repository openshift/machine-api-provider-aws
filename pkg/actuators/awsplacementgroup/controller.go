package awsplacementgroup

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	machinev1 "github.com/openshift/api/machine/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// Reconciler reconciles machineSets.
type Reconciler struct {
	Client client.Client
	Log    logr.Logger

	recorder record.EventRecorder
	scheme   *runtime.Scheme
}

// SetupWithManager creates a new controller for a manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&machinev1.AWSPlacementGroup{}).
		WithOptions(options).
		Build(r)

	if err != nil {
		return fmt.Errorf("failed setting up with a controller manager: %w", err)
	}

	r.recorder = mgr.GetEventRecorderFor("awsplacementgroup-controller")
	r.scheme = mgr.GetScheme()
	return nil
}

// Reconcile implements controller runtime Reconciler interface.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("name", req.Name, "namespace", req.Namespace)
	logger.V(3).Info("Reconciling")

	awsPlacementGroup := &machinev1.AWSPlacementGroup{}
	if err := r.Client.Get(ctx, req.NamespacedName, awsPlacementGroup); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, return. Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	// Ignore deleted AWSPlacementGroups, this can happen when foregroundDeletion
	// is enabled
	if !awsPlacementGroup.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	originalAWSPlacementGroupToPatch := client.MergeFrom(awsPlacementGroup.DeepCopy())

	result, err := r.reconcile(ctx, logger, awsPlacementGroup)
	if err != nil {
		logger.Error(err, "Failed to reconcile AWSPlacementGroup")
		r.recorder.Eventf(awsPlacementGroup, corev1.EventTypeWarning, "ReconcileError", "%v", err)
		// we don't return here so we want to attempt to patch the AWSPlacementGroup regardless of an error.
	}

	originalAWSPlacementGroupStatus := awsPlacementGroup.Status.DeepCopy()

	if err := r.Client.Patch(ctx, awsPlacementGroup, originalAWSPlacementGroupToPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch AWSPlacementGroup: %v", err)
	}

	awsPlacementGroup.Status = *originalAWSPlacementGroupStatus

	if err := r.Client.Status().Patch(ctx, awsPlacementGroup, originalAWSPlacementGroupToPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch AWSPlacementGroup Status: %v", err)
	}

	return result, err
}

func (r *Reconciler) reconcile(ctx context.Context, logger logr.Logger, awsPlacementGroup *machinev1.AWSPlacementGroup) (ctrl.Result, error) {
	logger.Info("reconciling AWSPlacementGroup")

	return ctrl.Result{}, nil
}
