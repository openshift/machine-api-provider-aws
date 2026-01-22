package machine

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
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
)

// allocateDedicatedHost allocates a new dedicated host for the given instance type in the specified availability zone.
// It applies any tags specified in the DynamicHostAllocation configuration.
func allocateDedicatedHost(client awsclient.Client, instanceType, availabilityZone string, tags map[string]string, machineName string) (string, error) {
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
		ec2Tags := make([]*ec2.Tag, 0, len(tags))
		for k, v := range tags {
			ec2Tags = append(ec2Tags, &ec2.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
		tagSpecs = append(tagSpecs, &ec2.TagSpecification{
			ResourceType: aws.String("dedicated-host"),
			Tags:         ec2Tags,
		})
		allocateInput.TagSpecifications = tagSpecs
	}

	output, err := client.AllocateHosts(allocateInput)
	if err != nil {
		klog.Errorf("Failed to allocate dedicated host: %v", err)
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

// describeDedicatedHost retrieves information about a dedicated host.
func describeDedicatedHost(client awsclient.Client, hostID string) (*ec2.Host, error) {
	describeInput := &ec2.DescribeHostsInput{
		HostIds: []*string{aws.String(hostID)},
	}

	output, err := client.DescribeHosts(describeInput)
	if err != nil {
		return nil, fmt.Errorf("failed to describe dedicated host %s: %w", hostID, err)
	}

	if len(output.Hosts) == 0 {
		return nil, fmt.Errorf("dedicated host %s not found", hostID)
	}

	return output.Hosts[0], nil
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
func getDynamicHostTags(placement *machinev1beta1.Placement) map[string]string {
	if placement == nil || placement.Host == nil || placement.Host.DedicatedHost == nil ||
		placement.Host.DedicatedHost.DynamicHostAllocation == nil {
		return nil
	}

	return placement.Host.DedicatedHost.DynamicHostAllocation.Tags
}

// getDynamicallyAllocatedHostID returns the host ID from the instance if it was dynamically allocated.
// It checks the machineProviderConfig to see if dynamic allocation was configured, and if so, returns the host ID from the instance.
func getDynamicallyAllocatedHostID(instance *ec2.Instance, machineProviderConfig *machinev1beta1.AWSMachineProviderConfig) string {
	// Check if dynamic allocation is configured
	if !shouldAllocateDedicatedHost(&machineProviderConfig.Placement) {
		return ""
	}

	// Get the host ID from the instance placement
	if instance == nil || instance.Placement == nil || instance.Placement.HostId == nil {
		return ""
	}

	return aws.StringValue(instance.Placement.HostId)
}
