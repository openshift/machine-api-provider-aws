/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package machine

import (
	"context"
	"errors"
	"fmt"
	"time"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	scopeFailFmt      = "%s: failed to create scope for machine: %w"
	reconcilerFailFmt = "%s: reconciler failed to %s machine: %w"
	createEventAction = "Create"
	updateEventAction = "Update"
	deleteEventAction = "Delete"
	noEventAction     = ""
	// dedicatedHostCapacityRequeueAfter is the initial delay before retrying
	// machine creation while it is blocked by insufficient dedicated host
	// capacity. AWS capacity droughts resolve on the order of minutes;
	// retrying much faster only hammers the AllocateHosts API without speeding
	// recovery.
	dedicatedHostCapacityRequeueAfter = 60 * time.Second
	// dedicatedHostCapacityRequeueMaxAfter caps the exponential growth of that
	// delay, so a long drought settles at a slow but steady retry rate instead
	// of polling AllocateHosts every minute for hours.
	dedicatedHostCapacityRequeueMaxAfter = 5 * time.Minute
)

// dedicatedHostCapacityRequeueDelay returns how long to wait before retrying a
// machine creation blocked by insufficient dedicated host capacity, given how
// long the MachineCreation condition has been failing. The delay doubles as
// the shortage goes on, up to dedicatedHostCapacityRequeueMaxAfter, so a short
// drought is retried quickly while a long one does not keep calling
// AllocateHosts at the base rate. There is deliberately no terminal deadline:
// capacity can come back at any time, and like any other transient create
// failure the machine keeps retrying until it is deleted or succeeds.
func dedicatedHostCapacityRequeueDelay(blockedFor time.Duration) time.Duration {
	delay := dedicatedHostCapacityRequeueAfter
	for delay < dedicatedHostCapacityRequeueMaxAfter && blockedFor >= 2*delay {
		delay *= 2
	}
	return min(delay, dedicatedHostCapacityRequeueMaxAfter)
}

// Actuator is responsible for performing machine reconciliation.
type Actuator struct {
	client              runtimeclient.Client
	eventRecorder       record.EventRecorder
	awsClientBuilder    awsclient.AwsClientBuilderFuncType
	configManagedClient runtimeclient.Client
	regionCache         awsclient.RegionCache
}

// ActuatorParams holds parameter information for Actuator.
type ActuatorParams struct {
	Client              runtimeclient.Client
	EventRecorder       record.EventRecorder
	AwsClientBuilder    awsclient.AwsClientBuilderFuncType
	ConfigManagedClient runtimeclient.Client
	RegionCache         awsclient.RegionCache
}

// NewActuator returns an actuator.
func NewActuator(params ActuatorParams) *Actuator {
	return &Actuator{
		client:              params.Client,
		eventRecorder:       params.EventRecorder,
		awsClientBuilder:    params.AwsClientBuilder,
		configManagedClient: params.ConfigManagedClient,
		regionCache:         params.RegionCache,
	}
}

// Set corresponding event based on error. It also returns the original error
// for convenience, so callers can do "return handleMachineError(...)".
func (a *Actuator) handleMachineError(machine *machinev1beta1.Machine, err error, eventAction string) error {
	klog.Errorf("%v error: %v", machine.GetName(), err)
	if eventAction != noEventAction {
		a.eventRecorder.Eventf(machine, corev1.EventTypeWarning, "Failed"+eventAction, "%v", err)
	}
	return err
}

// Create creates a machine and is invoked by the machine controller.
func (a *Actuator) Create(ctx context.Context, machine *machinev1beta1.Machine) error {
	klog.Infof("%s: actuator creating machine", machine.GetName())
	scope, err := newMachineScope(machineScopeParams{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		awsClientBuilder:    a.awsClientBuilder,
		configManagedClient: a.configManagedClient,
		regionCache:         a.regionCache,
	})
	if err != nil {
		fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
		return a.handleMachineError(machine, fmtErr, createEventAction)
	}
	defer scope.Close()

	// create() mutates the provider status conditions in place, so the
	// previous MachineCreation condition has to be captured before it runs: its
	// reason is used to emit the dedicated host capacity event only once per
	// shortage, and its transition time to back off retries during a long one.
	var prevMachineCreationReason string
	var machineCreationFailingSince time.Time
	if prevCondition := findCondition(scope.providerStatus.Conditions, string(machinev1beta1.MachineCreation)); prevCondition != nil {
		prevMachineCreationReason = prevCondition.Reason
		if prevCondition.Status == metav1.ConditionFalse && !prevCondition.LastTransitionTime.IsZero() {
			machineCreationFailingSince = prevCondition.LastTransitionTime.Time
		}
	}

	if err := newReconciler(scope).create(); err != nil {
		if err := scope.patchMachine(); err != nil {
			return err
		}
		var capacityErr *insufficientHostCapacityError
		if errors.As(err, &capacityErr) {
			// A dedicated host capacity shortage is retryable and expected to
			// resolve on its own. Requeue after a delay instead of returning
			// the error: an error would retry almost immediately (and would
			// emit a FailedCreate event on every attempt, which during a
			// multi-minute drought trips the pathological events monitor).
			// The shortage is surfaced once per transition via a distinct
			// event reason and via the DedicatedHostCapacityUnavailable
			// provider status condition, which also keeps retry attempts from
			// churning the Machine status.
			// The retry delay grows with how long the MachineCreation condition
			// has been failing, which survives actuator restarts because it is
			// persisted in the Machine status.
			// Note: if the process restarts mid-shortage the event is not
			// re-emitted; the raw AWS error is still logged on every attempt.
			if prevMachineCreationReason != DedicatedHostCapacityUnavailableConditionReason {
				a.eventRecorder.Eventf(machine, corev1.EventTypeWarning, insufficientDedicatedHostCapacityEventReason,
					"%v (AWS: %v)", capacityErr, capacityErr.Unwrap())
			}
			var blockedFor time.Duration
			if !machineCreationFailingSince.IsZero() {
				blockedFor = time.Since(machineCreationFailingSince)
			}
			requeueAfter := dedicatedHostCapacityRequeueDelay(blockedFor)
			klog.Infof("%s: machine creation blocked by dedicated host capacity for %s, requeueing in %s: %v",
				machine.GetName(), blockedFor.Round(time.Second), requeueAfter, capacityErr)
			return &machinecontroller.RequeueAfterError{RequeueAfter: requeueAfter}
		}
		fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), createEventAction, err)
		return a.handleMachineError(machine, fmtErr, createEventAction)
	}
	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, createEventAction, "Created Machine %v", machine.GetName())
	return scope.patchMachine()
}

// Exists determines if the given machine currently exists.
// A machine which is not terminated is considered as existing.
func (a *Actuator) Exists(ctx context.Context, machine *machinev1beta1.Machine) (bool, error) {
	klog.Infof("%s: actuator checking if machine exists", machine.GetName())
	scope, err := newMachineScope(machineScopeParams{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		awsClientBuilder:    a.awsClientBuilder,
		configManagedClient: a.configManagedClient,
		regionCache:         a.regionCache,
	})
	if err != nil {
		return false, fmt.Errorf(scopeFailFmt, machine.GetName(), err)
	}
	defer scope.Close()
	return newReconciler(scope).exists()
}

// Update attempts to sync machine state with an existing instance.
func (a *Actuator) Update(ctx context.Context, machine *machinev1beta1.Machine) error {
	klog.Infof("%s: actuator updating machine", machine.GetName())
	scope, err := newMachineScope(machineScopeParams{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		awsClientBuilder:    a.awsClientBuilder,
		configManagedClient: a.configManagedClient,
		regionCache:         a.regionCache,
	})
	if err != nil {
		fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
		return a.handleMachineError(machine, fmtErr, updateEventAction)
	}
	defer scope.Close()
	if err := newReconciler(scope).update(); err != nil {
		// Update machine and machine status in case it was modified
		if err := scope.patchMachine(); err != nil {
			return err
		}
		fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), updateEventAction, err)
		return a.handleMachineError(machine, fmtErr, updateEventAction)
	}

	previousResourceVersion := scope.machine.ResourceVersion

	if err := scope.patchMachine(); err != nil {
		return err
	}

	currentResourceVersion := scope.machine.ResourceVersion

	// Create event only if machine object was modified
	if previousResourceVersion != currentResourceVersion {
		a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, updateEventAction, "Updated Machine %v", machine.GetName())
	}

	return nil
}

// Delete deletes a machine and updates its finalizer
func (a *Actuator) Delete(ctx context.Context, machine *machinev1beta1.Machine) error {
	klog.Infof("%s: actuator deleting machine", machine.GetName())
	scope, err := newMachineScope(machineScopeParams{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		awsClientBuilder:    a.awsClientBuilder,
		configManagedClient: a.configManagedClient,
		regionCache:         a.regionCache,
	})
	if err != nil {
		fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
		return a.handleMachineError(machine, fmtErr, deleteEventAction)
	}
	defer scope.Close()
	if err := newReconciler(scope).delete(); err != nil {
		if err := scope.patchMachine(); err != nil {
			return err
		}
		fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), deleteEventAction, err)
		return a.handleMachineError(machine, fmtErr, deleteEventAction)
	}
	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, deleteEventAction, "Deleted machine %v", machine.GetName())
	return scope.patchMachine()
}
