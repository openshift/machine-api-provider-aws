package machine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	awserr "github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	mockaws "github.com/openshift/machine-api-provider-aws/pkg/client/mock"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	// Add types to scheme
	machinev1beta1.AddToScheme(scheme.Scheme)
	machinev1.Install(scheme.Scheme)
	configv1.AddToScheme(scheme.Scheme)

	ctrl.SetLogger(klogr.New())
}

func TestMachineEvents(t *testing.T) {
	instanceID := "i-02fcb933c5da7085c"
	g := NewWithT(t)

	awsCredentialsSecret := stubAwsCredentialsSecret()
	g.Expect(k8sClient.Create(context.TODO(), awsCredentialsSecret)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(context.TODO(), awsCredentialsSecret)).To(Succeed())
	}()

	userDataSecret := stubUserDataSecret()
	g.Expect(k8sClient.Create(context.TODO(), userDataSecret)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(context.TODO(), userDataSecret)).To(Succeed())
	}()

	cases := []struct {
		name                string
		error               string
		operation           func(actuator *Actuator, machine *machinev1beta1.Machine)
		event               string
		awsError            bool
		invalidMachineScope bool
		setInstanceID       bool
	}{
		{
			name: "Create machine event failed on invalid machine scope",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Create(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: failed to create scope for machine: failed to create aws client: AWS client error",
			invalidMachineScope: true,
			awsError:            false,
		},
		{
			name: "Create machine event failed, reconciler's create failed",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Create(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: reconciler failed to Create machine: unable to remove stopped machines: error getting stopped instances: AWS error",
			invalidMachineScope: false,
			awsError:            true,
		},
		{
			name: "Create machine event succeed",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Create(context.TODO(), machine)
			},
			event:               "Created Machine aws-actuator-testing-machine",
			invalidMachineScope: false,
			awsError:            false,
		},
		{
			name: "Update machine event failed on invalid machine scope",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Update(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: failed to create scope for machine: failed to create aws client: AWS client error",
			invalidMachineScope: true,
			awsError:            false,
		},
		{
			name: "Update machine event failed, reconciler's update failed",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Update(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: reconciler failed to Update machine: AWS error",
			invalidMachineScope: false,
			awsError:            true,
		},
		{
			name: "Update machine event succeed and only one event is created",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Update(context.TODO(), machine)
				actuator.Update(context.TODO(), machine)
			},
			setInstanceID: true, // This implies the machine was already created
			event:         "Updated Machine aws-actuator-testing-machine",
		},
		{
			name: "Delete machine event failed on invalid machine scope",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Delete(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: failed to create scope for machine: failed to create aws client: AWS client error",
			invalidMachineScope: true,
			awsError:            false,
		},
		{
			name: "Delete machine event failed, reconciler's delete failed",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Delete(context.TODO(), machine)
			},
			event:               "aws-actuator-testing-machine: reconciler failed to Delete machine: AWS error",
			invalidMachineScope: false,
			awsError:            true,
		},
		{
			name: "Delete machine event succeed",
			operation: func(actuator *Actuator, machine *machinev1beta1.Machine) {
				actuator.Delete(context.TODO(), machine)
			},
			setInstanceID:       true, // This implies the machine was already created
			event:               "Deleted machine aws-actuator-testing-machine",
			invalidMachineScope: false,
			awsError:            false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.TODO()
			gs := NewWithT(t)

			machine, err := stubMachine()
			gs.Expect(err).ToNot(HaveOccurred())
			gs.Expect(stubMachine).ToNot(BeNil())

			// Clean up events to make sure we have a clean slate.
			eventList := &v1.EventList{}
			gs.Expect(k8sClient.List(ctx, eventList, client.InNamespace(machine.Namespace))).To(Succeed())
			for i := range eventList.Items {
				gs.Expect(k8sClient.Delete(ctx, &eventList.Items[i])).To(Succeed())
			}

			// Create the machine
			gs.Expect(k8sClient.Create(ctx, machine)).To(Succeed())
			defer func() {
				gs.Expect(k8sClient.Delete(ctx, machine)).To(Succeed())
			}()

			// Create infrastructure object
			infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: awsclient.GlobalInfrastuctureName}}
			gs.Expect(k8sClient.Create(ctx, infra)).To(Succeed())
			defer func() {
				gs.Expect(k8sClient.Delete(ctx, infra)).To(Succeed())
			}()

			// Ensure the machine has synced to the cache
			getMachine := func() error {
				machineKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Name}
				return k8sClient.Get(ctx, machineKey, machine)
			}
			gs.Eventually(getMachine, timeout).Should(Succeed())

			mockCtrl := gomock.NewController(t)
			mockAWSClient := mockaws.NewMockClient(mockCtrl)
			awsClientBuilder := func(client runtimeclient.Client, secretName, namespace, region string, configManagedClient runtimeclient.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
				return mockAWSClient, nil
			}
			if tc.invalidMachineScope {
				awsClientBuilder = func(client runtimeclient.Client, secretName, namespace, region string, configManagedClient runtimeclient.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
					return nil, errors.New("AWS client error")
				}
			}

			if tc.setInstanceID {
				updateInstanceID := func() error {
					if getMachine(); err != nil {
						return err
					}

					ps, err := ProviderStatusFromRawExtension(machine.Status.ProviderStatus)
					if err != nil {
						return err
					}
					ps.InstanceID = &instanceID

					raw, err := RawExtensionFromProviderStatus(ps)
					if err != nil {
						return err
					}
					machine.Status.ProviderStatus = raw

					return k8sClient.Status().Update(ctx, machine)
				}
				gs.Eventually(updateInstanceID, timeout).Should(Succeed())
			}

			if tc.awsError {
				mockAWSClient.EXPECT().DescribeInstances(gomock.Any()).Return(nil, errors.New("AWS error")).AnyTimes()
			} else {
				mockAWSClient.EXPECT().DescribeInstances(stubDescribeInstancesInput(instanceID)).Return(stubDescribeInstancesOutput("ami-a9acbbd6", instanceID, ec2.InstanceStateNameRunning, "192.168.0.10"), nil).AnyTimes()
				mockAWSClient.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{}, nil).AnyTimes()
			}

			mockAWSClient.EXPECT().RunInstances(gomock.Any()).Return(stubReservation("ami-a9acbbd6", instanceID, "192.168.0.10"), nil).AnyTimes()
			mockAWSClient.EXPECT().TerminateInstances(gomock.Any()).Return(&ec2.TerminateInstancesOutput{}, nil).AnyTimes()
			mockAWSClient.EXPECT().RegisterInstancesWithLoadBalancer(gomock.Any()).Return(nil, nil).AnyTimes()
			mockAWSClient.EXPECT().TerminateInstances(gomock.Any()).Return(&ec2.TerminateInstancesOutput{}, nil).AnyTimes()
			mockAWSClient.EXPECT().RegisterInstancesWithLoadBalancer(gomock.Any()).Return(nil, nil).AnyTimes()
			mockAWSClient.EXPECT().ELBv2DescribeLoadBalancers(gomock.Any()).Return(stubDescribeLoadBalancersOutput(), nil).AnyTimes()
			mockAWSClient.EXPECT().ELBv2DescribeTargetGroups(gomock.Any()).Return(stubDescribeTargetGroupsOutput(), nil).AnyTimes()
			mockAWSClient.EXPECT().ELBv2RegisterTargets(gomock.Any()).Return(nil, nil).AnyTimes()
			mockAWSClient.EXPECT().ELBv2DescribeTargetHealth(gomock.Any()).Return(stubDescribeTargetHealthOutput(), nil).AnyTimes()
			mockAWSClient.EXPECT().ELBv2DeregisterTargets(gomock.Any()).Return(nil, nil).AnyTimes()
			mockAWSClient.EXPECT().DescribeVpcs(gomock.Any()).Return(StubDescribeVPCs()).AnyTimes()
			mockAWSClient.EXPECT().DescribeDHCPOptions(gomock.Any()).Return(StubDescribeDHCPOptions()).AnyTimes()
			mockAWSClient.EXPECT().CreateTags(gomock.Any()).Return(&ec2.CreateTagsOutput{}, nil).AnyTimes()
			mockAWSClient.EXPECT().DescribeSubnets(gomock.Any()).Return(stubDescribeSubnetsOutput(), nil).AnyTimes()
			mockAWSClient.EXPECT().DescribeAvailabilityZones(gomock.Any()).Return(stubDescribeAvailabilityZonesOutput(), nil).AnyTimes()

			params := ActuatorParams{
				Client:           k8sClient,
				EventRecorder:    eventRecorder,
				AwsClientBuilder: awsClientBuilder,
			}
			actuator := NewActuator(params)
			tc.operation(actuator, machine)

			waitForEvent := func(g Gomega) error {
				g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(machine.Namespace))).To(Succeed())
				if len(eventList.Items) != 1 {
					errorMsg := fmt.Sprintf("Expected len 1, got %d", len(eventList.Items))
					return errors.New(errorMsg)
				}
				return nil
			}

			gs.Eventually(waitForEvent, timeout).Should(Succeed())

			gs.Expect(eventList.Items[0].Message).To(Equal(tc.event))
		})
	}
}

// TestCreateDedicatedHostCapacityRequeue verifies that when machine creation
// is blocked by an AWS dedicated host capacity shortage, Create requeues after
// a delay instead of erroring out, and emits the distinct
// InsufficientDedicatedHostCapacity event exactly once per shortage (and no
// FailedCreate events), so a multi-minute drought cannot trip the
// pathological-events monitor.
func TestCreateDedicatedHostCapacityRequeue(t *testing.T) {
	ctx := context.TODO()
	g := NewWithT(t)

	awsCredentialsSecret := stubAwsCredentialsSecret()
	g.Expect(k8sClient.Create(ctx, awsCredentialsSecret)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(ctx, awsCredentialsSecret)).To(Succeed())
	}()

	userDataSecret := stubUserDataSecret()
	g.Expect(k8sClient.Create(ctx, userDataSecret)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(ctx, userDataSecret)).To(Succeed())
	}()

	machine, err := stubMachine()
	g.Expect(err).ToNot(HaveOccurred())

	machinePc, err := ProviderSpecFromRawExtension(machine.Spec.ProviderSpec.Value)
	g.Expect(err).ToNot(HaveOccurred())
	machinePc.Placement.Host = &machinev1beta1.HostPlacement{
		Affinity: ptr.To(machinev1beta1.HostAffinityDedicatedHost),
		DedicatedHost: &machinev1beta1.DedicatedHost{
			AllocationStrategy: ptr.To(AllocationStrategyDynamic),
		},
	}
	encodedProviderConfig, err := RawExtensionFromProviderSpec(machinePc)
	g.Expect(err).ToNot(HaveOccurred())
	machine.Spec.ProviderSpec = machinev1beta1.ProviderSpec{Value: encodedProviderConfig}

	// Clean up events to make sure we have a clean slate.
	eventList := &v1.EventList{}
	g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(machine.Namespace))).To(Succeed())
	for i := range eventList.Items {
		g.Expect(k8sClient.Delete(ctx, &eventList.Items[i])).To(Succeed())
	}

	g.Expect(k8sClient.Create(ctx, machine)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(ctx, machine)).To(Succeed())
	}()

	// Create infrastructure object
	infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: awsclient.GlobalInfrastuctureName}}
	g.Expect(k8sClient.Create(ctx, infra)).To(Succeed())
	defer func() {
		g.Expect(k8sClient.Delete(ctx, infra)).To(Succeed())
	}()

	// Ensure the machine has synced to the cache
	getMachine := func() error {
		machineKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Name}
		return k8sClient.Get(ctx, machineKey, machine)
	}
	g.Expect(getMachine()).To(Succeed())

	mockCtrl := gomock.NewController(t)
	mockAWSClient := mockaws.NewMockClient(mockCtrl)
	mockAWSClient.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{}, nil).AnyTimes()
	mockAWSClient.EXPECT().DescribeSubnets(gomock.Any()).Return(stubDescribeSubnetsOutput(), nil).AnyTimes()
	mockAWSClient.EXPECT().DescribeAvailabilityZones(gomock.Any()).Return(stubDescribeAvailabilityZonesOutput(), nil).AnyTimes()
	mockAWSClient.EXPECT().AllocateHosts(gomock.Any()).Return(nil, awserr.New(insufficientHostCapacityErrorCode, "We currently do not have sufficient dedicated host capacity", nil)).AnyTimes()

	params := ActuatorParams{
		Client:        k8sClient,
		EventRecorder: eventRecorder,
		AwsClientBuilder: func(client runtimeclient.Client, secretName, namespace, region string, configManagedClient runtimeclient.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
			return mockAWSClient, nil
		},
	}
	actuator := NewActuator(params)

	assertRequeueAfterError := func(err error) {
		var requeueErr *machinecontroller.RequeueAfterError
		g.Expect(errors.As(err, &requeueErr)).To(BeTrue())
		g.Expect(requeueErr.RequeueAfter).To(Equal(dedicatedHostCapacityRequeueAfter))
	}

	// Both attempts hit the capacity shortage and requeue after a delay
	// instead of returning an error (which would emit a FailedCreate event on
	// every attempt).
	assertRequeueAfterError(actuator.Create(ctx, machine))

	// Pick up the machine as patched by the first attempt, like the machine
	// controller would on the next reconcile: its next attempt only runs once
	// the patched machine has reached the informer cache.
	getPatchedMachine := func() error {
		if err := getMachine(); err != nil {
			return err
		}
		providerStatus, err := ProviderStatusFromRawExtension(machine.Status.ProviderStatus)
		if err != nil {
			return err
		}
		condition := findCondition(providerStatus.Conditions, string(machinev1beta1.MachineCreation))
		if condition == nil || condition.Reason != DedicatedHostCapacityUnavailableConditionReason {
			return errors.New("machine creation condition with the capacity reason is not yet visible")
		}
		return nil
	}
	g.Eventually(getPatchedMachine, timeout).Should(Succeed())
	assertRequeueAfterError(actuator.Create(ctx, machine))

	// Exactly one distinct event is emitted for the whole shortage, with the
	// dedicated reason rather than FailedCreate. The event correlator dedupes
	// identical emissions into a single Event with an incremented Count, so a
	// duplicate emission would still pass a length check alone.
	waitForEvent := func(g Gomega) error {
		g.Expect(k8sClient.List(ctx, eventList, client.InNamespace(machine.Namespace))).To(Succeed())
		if len(eventList.Items) != 1 {
			return fmt.Errorf("expected exactly 1 event, got %d", len(eventList.Items))
		}
		if eventList.Items[0].Count != 1 {
			return fmt.Errorf("expected event count 1, got %d", eventList.Items[0].Count)
		}
		return nil
	}
	g.Eventually(waitForEvent, timeout).Should(Succeed())
	// A duplicate emission from the second Create attempt could land shortly
	// after Eventually first observes the event.
	g.Consistently(waitForEvent, 2*time.Second).Should(Succeed())

	g.Expect(eventList.Items[0].Reason).To(Equal(insufficientDedicatedHostCapacityEventReason))
	g.Expect(eventList.Items[0].Type).To(Equal(v1.EventTypeWarning))
	g.Expect(eventList.Items[0].Message).To(ContainSubstring(insufficientHostCapacityErrorCode))

	// The persisted MachineCreation condition carries the distinct reason.
	g.Expect(getMachine()).To(Succeed())
	providerStatus, err := ProviderStatusFromRawExtension(machine.Status.ProviderStatus)
	g.Expect(err).ToNot(HaveOccurred())
	condition := findCondition(providerStatus.Conditions, string(machinev1beta1.MachineCreation))
	g.Expect(condition).ToNot(BeNil())
	g.Expect(condition.Reason).To(Equal(DedicatedHostCapacityUnavailableConditionReason))
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
}

func TestHandleMachineErrors(t *testing.T) {
	machine, err := stubMachine()
	if err != nil {
		t.Fatal(err)
	}

	mockCtrl := gomock.NewController(t)
	mockAWSClient := mockaws.NewMockClient(mockCtrl)

	cases := []struct {
		name        string
		eventAction string
		event       string
	}{
		{
			name:        "Create event when event action is present",
			eventAction: "testAction",
			event:       "Warning FailedtestAction testError",
		},
		{
			name:        "Don't event when there is no event action",
			eventAction: "",
		},
	}

	mockAWSClient.EXPECT().DescribeSubnets(gomock.Any()).Return(&ec2.DescribeSubnetsOutput{}, nil).AnyTimes()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventsChannel := make(chan string, 1)

			params := ActuatorParams{
				// use fake recorder and store an event into one item long buffer for subsequent check
				EventRecorder: &record.FakeRecorder{
					Events: eventsChannel,
				},
			}

			actuator := NewActuator(params)

			actuator.handleMachineError(machine, errors.New("testError"), tc.eventAction)

			select {
			case event := <-eventsChannel:
				if event != tc.event {
					t.Errorf("Expected %q event, got %q", tc.event, event)
				}
			default:
				if tc.event != "" {
					t.Errorf("Expected %q event, got none", tc.event)
				}
			}
		})
	}
}
