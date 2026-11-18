package machine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	"k8s.io/klog/v2"
)

const (
	// AllocationStrategyDynamic represents the dynamic allocation strategy constant.
	AllocationStrategyDynamic = machinev1beta1.AllocationStrategy("Dynamic")
	// AllocationStrategyUserProvided represents the user-provided allocation strategy constant.
	AllocationStrategyUserProvided = machinev1beta1.AllocationStrategy("UserProvided")

	// insufficientHostCapacityErrorCode is the AWS EC2 error code returned by
	// AllocateHosts when there is currently no dedicated host capacity for the
	// requested instance type and availability zone. The vendored aws-sdk-go
	// does not define a constant for it.
	insufficientHostCapacityErrorCode = "InsufficientHostCapacity"

	// DedicatedHostCapacityUnavailableConditionReason is the MachineCreation
	// condition reason used when machine creation is blocked by a lack of AWS
	// dedicated host capacity. Unlike MachineCreationFailed, this state is
	// retryable and expected to resolve on its own once AWS provisions more
	// capacity, so the machine is not failed and creation keeps being retried.
	DedicatedHostCapacityUnavailableConditionReason string = "DedicatedHostCapacityUnavailable"

	// insufficientDedicatedHostCapacityEventReason is the event reason used
	// for the (one-shot) event emitted when a machine creation is first
	// blocked by insufficient dedicated host capacity. It is deliberately
	// distinct from FailedCreate so capacity shortages can be told apart
	// from genuine create failures.
	insufficientDedicatedHostCapacityEventReason = "InsufficientDedicatedHostCapacity"
)

// insufficientHostCapacityError is returned when AllocateHosts fails because
// AWS currently has no dedicated host capacity for the requested instance
// type and availability zone. It is a retryable condition: the machine is not
// failed and creation is requeued after a delay instead of erroring out.
//
// Error() must stay byte-stable across attempts: the message is persisted in
// the Machine provider status condition, and any per-attempt variation (e.g.
// the AZ hints AWS includes in the raw error, which differ between calls)
// would update the condition on every attempt, churn the Machine status and
// requeue the controller through watch events on every attempt, defeating
// the requeue delay. The varying AWS detail remains available via Unwrap()
// and is logged by allocateDedicatedHost on every attempt.
type insufficientHostCapacityError struct {
	cause            error
	instanceType     string
	availabilityZone string
}

func newInsufficientHostCapacityError(cause error, instanceType, availabilityZone string) *insufficientHostCapacityError {
	return &insufficientHostCapacityError{
		cause:            cause,
		instanceType:     instanceType,
		availabilityZone: availabilityZone,
	}
}

func (e *insufficientHostCapacityError) Error() string {
	return fmt.Sprintf("%s: insufficient dedicated host capacity for instance type %s in availability zone %s, will retry",
		insufficientHostCapacityErrorCode, e.instanceType, e.availabilityZone)
}

func (e *insufficientHostCapacityError) Unwrap() error {
	return e.cause
}

// isInsufficientHostCapacityError reports whether err is an AWS
// InsufficientHostCapacity error, matching either the machine-readable error
// code or, defensively, the code appearing in the error message.
func isInsufficientHostCapacityError(err error) bool {
	if err == nil {
		return false
	}
	var awsErr awserr.Error
	if errors.As(err, &awsErr) && awsErr.Code() == insufficientHostCapacityErrorCode {
		return true
	}
	return strings.Contains(err.Error(), insufficientHostCapacityErrorCode)
}

// allocateDedicatedHost allocates a new dedicated host for the given instance type in the specified availability zone.
// It applies any tags specified in the DynamicHostAllocation configuration.
func allocateDedicatedHost(client awsclient.Client, instanceType, availabilityZone string, tags []*ec2.Tag, machineName string) (string, error) {
	klog.Infof("Allocating dedicated host for instance type %s in availability zone %s for machine %s", instanceType, availabilityZone, machineName)

	allocateInput := &ec2.AllocateHostsInput{
		InstanceType:     aws.String(instanceType),
		AvailabilityZone: aws.String(availabilityZone),
		Quantity:         aws.Int64(1),
		AutoPlacement:    aws.String("off"), // Disable auto-placement to ensure 1:1 mapping
	}

	// Add tags if provided
	if len(tags) > 0 {
		var tagSpecs []*ec2.TagSpecification
		tagSpecs = append(tagSpecs, &ec2.TagSpecification{
			ResourceType: aws.String("dedicated-host"),
			Tags:         tags,
		})
		allocateInput.TagSpecifications = tagSpecs
	}

	output, err := client.AllocateHosts(allocateInput)
	if err != nil {
		klog.Errorf("Failed to allocate dedicated host: %v", err)
		if isInsufficientHostCapacityError(err) {
			// Retryable capacity shortage: wrap it in a typed error with a
			// stable message so callers can distinguish it from genuine
			// failures and requeue with a delay instead of failing the
			// machine.
			return "", newInsufficientHostCapacityError(err, instanceType, availabilityZone)
		}
		return "", fmt.Errorf("failed to allocate dedicated host: %w", err)
	}

	if len(output.HostIds) == 0 {
		return "", fmt.Errorf("no host IDs returned from AllocateHosts")
	}

	hostID := aws.StringValue(output.HostIds[0])
	klog.Infof("Successfully allocated dedicated host %s for machine %s", hostID, machineName)
	return hostID, nil
}

// releaseDedicatedHost releases the dedicated host with the given ID.
func releaseDedicatedHost(client awsclient.Client, hostID, machineName string) error {
	klog.Infof("Releasing dedicated host %s for machine %s", hostID, machineName)

	releaseInput := &ec2.ReleaseHostsInput{
		HostIds: []*string{aws.String(hostID)},
	}

	output, err := client.ReleaseHosts(releaseInput)
	if err != nil {
		klog.Errorf("Failed to release dedicated host %s: %v", hostID, err)
		return fmt.Errorf("failed to release dedicated host %s: %w", hostID, err)
	}

	// Check if there were any failures
	if len(output.Unsuccessful) > 0 {
		klog.Errorf("Failed to release dedicated host %s: %v", hostID, aws.StringValue(output.Unsuccessful[0].Error.Message))
		return fmt.Errorf("failed to release dedicated host %s: %s", hostID, aws.StringValue(output.Unsuccessful[0].Error.Message))
	}

	klog.Infof("Successfully released dedicated host %s for machine %s", hostID, machineName)
	return nil
}

// shouldAllocateDedicatedHost checks if a dedicated host should be allocated based on the placement configuration.
func shouldAllocateDedicatedHost(placement *machinev1beta1.Placement) bool {
	if placement == nil || placement.Host == nil || placement.Host.DedicatedHost == nil {
		return false
	}

	// If AllocationStrategy is nil, default is UserProvided, so we don't allocate
	if placement.Host.DedicatedHost.AllocationStrategy == nil {
		return false
	}

	return *placement.Host.DedicatedHost.AllocationStrategy == AllocationStrategyDynamic
}

// getDedicatedHostID returns the dedicated host ID from the placement configuration if it's user-provided.
func getDedicatedHostID(placement *machinev1beta1.Placement) string {
	if placement == nil || placement.Host == nil || placement.Host.DedicatedHost == nil {
		return ""
	}

	// If AllocationStrategy is nil or UserProvided, return the ID
	if placement.Host.DedicatedHost.AllocationStrategy == nil ||
		*placement.Host.DedicatedHost.AllocationStrategy == AllocationStrategyUserProvided {
		return placement.Host.DedicatedHost.ID
	}

	return ""
}

// getDynamicHostTags returns the tags to apply to a dynamically allocated dedicated host.
func getDynamicHostTags(placement *machinev1beta1.Placement) []machinev1beta1.TagSpecification {
	if placement == nil || placement.Host == nil || placement.Host.DedicatedHost == nil ||
		placement.Host.DedicatedHost.DynamicHostAllocation == nil ||
		placement.Host.DedicatedHost.DynamicHostAllocation.Tags == nil {
		return nil
	}

	return *placement.Host.DedicatedHost.DynamicHostAllocation.Tags
}

// getAllocatedHostIDFromStatus returns the dynamically allocated host ID from the provider status.
func getAllocatedHostIDFromStatus(providerStatus *machinev1beta1.AWSMachineProviderStatus) string {
	if providerStatus == nil || providerStatus.DedicatedHost == nil || providerStatus.DedicatedHost.ID == "" {
		return ""
	}
	return providerStatus.DedicatedHost.ID
}

// setAllocatedHostIDInStatus sets the dynamically allocated host ID in the provider status.
func setAllocatedHostIDInStatus(providerStatus *machinev1beta1.AWSMachineProviderStatus, hostID string) {
	if providerStatus.DedicatedHost == nil {
		providerStatus.DedicatedHost = &machinev1beta1.DedicatedHostStatus{}
	}
	providerStatus.DedicatedHost.ID = hostID
}

// clearAllocatedHostIDInStatus clears the dynamically allocated host ID from the provider status.
func clearAllocatedHostIDInStatus(providerStatus *machinev1beta1.AWSMachineProviderStatus) {
	if providerStatus.DedicatedHost != nil {
		providerStatus.DedicatedHost.ID = ""
	}
}

// tagBYODedicatedHost tags a BYO (Bring Your Own) dedicated host with kubernetes.io/cluster/<cluster-id>=shared.
func tagBYODedicatedHost(client awsclient.Client, hostID, clusterID, machineName string) error {
	klog.Infof("Tagging BYO dedicated host %s with kubernetes.io/cluster/%s=shared for machine %s", hostID, clusterID, machineName)

	tags := []*ec2.Tag{
		{
			Key:   aws.String("kubernetes.io/cluster/" + clusterID),
			Value: aws.String("shared"),
		},
	}

	input := &ec2.CreateTagsInput{
		Resources: []*string{aws.String(hostID)},
		Tags:      tags,
	}

	_, err := client.CreateTags(input)
	if err != nil {
		klog.Errorf("Failed to tag BYO dedicated host %s: %v", hostID, err)
		return fmt.Errorf("failed to tag BYO dedicated host %s: %w", hostID, err)
	}

	klog.Infof("Successfully tagged BYO dedicated host %s for machine %s", hostID, machineName)
	return nil
}

// isBYODedicatedHost checks if a placement configuration uses a BYO dedicated host.
func isBYODedicatedHost(placement *machinev1beta1.Placement) bool {
	if placement == nil || placement.Host == nil || placement.Host.DedicatedHost == nil {
		return false
	}

	// BYO if AllocationStrategy is nil (default is UserProvided) or explicitly set to UserProvided
	// and a host ID is provided
	if placement.Host.DedicatedHost.ID == "" {
		return false
	}

	if placement.Host.DedicatedHost.AllocationStrategy == nil ||
		*placement.Host.DedicatedHost.AllocationStrategy == AllocationStrategyUserProvided {
		return true
	}

	return false
}
