package machine

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/openshift/machine-api-operator/pkg/metrics"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	"k8s.io/klog/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	zoneTypeWavelengthZone = "wavelength-zone"
)

// Scan machine tags, and return a deduped tags list. The first found value gets precedence.
func removeDuplicatedTags(tags []*ec2.Tag) []*ec2.Tag {
	m := make(map[string]bool)
	result := []*ec2.Tag{}

	// look for duplicates
	for _, entry := range tags {
		if _, value := m[*entry.Key]; !value {
			m[*entry.Key] = true
			result = append(result, entry)
		}
	}
	return result
}

// removeStoppedMachine removes all instances of a specific machine that are in a stopped state.
func removeStoppedMachine(machine *machinev1beta1.Machine, client awsclient.Client) error {
	instances, err := getStoppedInstances(machine, client)
	if err != nil {
		klog.Errorf("Error getting stopped instances: %v", err)
		return fmt.Errorf("error getting stopped instances: %v", err)
	}

	if len(instances) == 0 {
		klog.Infof("No stopped instances found for machine %v", machine.Name)
		return nil
	}

	_, err = terminateInstances(client, instances)
	return err
}

func buildEC2Filters(inputFilters []machinev1beta1.Filter) []*ec2.Filter {
	filters := make([]*ec2.Filter, len(inputFilters))
	for i, f := range inputFilters {
		values := make([]*string, len(f.Values))
		for j, v := range f.Values {
			values[j] = aws.String(v)
		}
		filters[i] = &ec2.Filter{
			Name:   aws.String(f.Name),
			Values: values,
		}
	}
	return filters
}

func getSecurityGroupsIDs(securityGroups []machinev1beta1.AWSResourceReference, client awsclient.Client) ([]*string, error) {
	var securityGroupIDs []*string
	for _, g := range securityGroups {
		// ID has priority
		if g.ID != nil {
			securityGroupIDs = append(securityGroupIDs, g.ID)
		} else if g.Filters != nil {
			klog.Info("Describing security groups based on filters")
			// Get groups based on filters
			describeSecurityGroupsRequest := ec2.DescribeSecurityGroupsInput{
				Filters: buildEC2Filters(g.Filters),
			}
			describeSecurityGroupsResult, err := client.DescribeSecurityGroups(&describeSecurityGroupsRequest)
			if err != nil {
				klog.Errorf("error describing security groups: %v", err)
				return nil, fmt.Errorf("error describing security groups: %v", err)
			}
			for _, g := range describeSecurityGroupsResult.SecurityGroups {
				groupID := *g.GroupId
				securityGroupIDs = append(securityGroupIDs, &groupID)
			}
		}
	}

	if len(securityGroupIDs) == 0 {
		klog.Error("No security group found")
		return nil, fmt.Errorf("no security group found")
	}

	return securityGroupIDs, nil
}

func getSubnetIDs(machine runtimeclient.ObjectKey, subnet machinev1beta1.AWSResourceReference, availabilityZone string, client awsclient.Client) ([]*string, error) {
	var subnetIDs []*string
	// ID has priority
	if subnet.ID != nil {
		subnetIDs = append(subnetIDs, subnet.ID)

		availabilityZoneFromSubnetID, err := getAvalabilityZoneFromSubnetID(*subnet.ID, client)
		if err != nil {
			klog.Errorf("could not check if the subnet id and availability zone fields are mismatched: %v", err)
			return subnetIDs, nil
		}

		if availabilityZone != availabilityZoneFromSubnetID {
			klog.Errorf("mismatched subnet id %s and availibity zone %s", *subnet.ID, availabilityZone)
		}
	} else {
		var filters []machinev1beta1.Filter
		if availabilityZone != "" {
			// Improve error logging for better user experience.
			// Otherwise, during the process of minimizing API calls, this is a good
			// candidate for removal.
			_, err := client.DescribeAvailabilityZones(&ec2.DescribeAvailabilityZonesInput{
				ZoneNames: []*string{aws.String(availabilityZone)},
			})
			if err != nil {
				metrics.RegisterFailedInstanceCreate(&metrics.MachineLabels{
					Name:      machine.Name,
					Namespace: machine.Namespace,
					Reason:    "error describing availability zones",
				})
				klog.Errorf("error describing availability zones: %v", err)
				return nil, fmt.Errorf("error describing availability zones: %v", err)
			}
			filters = append(filters, machinev1beta1.Filter{Name: "availabilityZone", Values: []string{availabilityZone}})
		}
		filters = append(filters, subnet.Filters...)
		klog.Info("Describing subnets based on filters")
		describeSubnetRequest := ec2.DescribeSubnetsInput{
			Filters: buildEC2Filters(filters),
		}
		describeSubnetResult, err := client.DescribeSubnets(&describeSubnetRequest)
		if err != nil {
			metrics.RegisterFailedInstanceCreate(&metrics.MachineLabels{
				Name:      machine.Name,
				Namespace: machine.Namespace,
				Reason:    "error describing subnets",
			})
			klog.Errorf("error describing subnets: %v", err)
			return nil, fmt.Errorf("error describing subnets: %v", err)
		}
		for _, n := range describeSubnetResult.Subnets {
			subnetID := *n.SubnetId
			subnetIDs = append(subnetIDs, &subnetID)
		}
	}
	if len(subnetIDs) == 0 {
		return nil, fmt.Errorf("no subnet IDs were found")
	}

	return subnetIDs, nil
}

// getAvalabilityZoneFromSubnetID gets an availability zone from specified subnet id.
func getAvalabilityZoneFromSubnetID(subnetID string, client awsclient.Client) (string, error) {
	result, err := client.DescribeSubnets(&ec2.DescribeSubnetsInput{
		DryRun: aws.Bool(false),
		SubnetIds: []*string{
			aws.String(subnetID),
		},
	})
	if err != nil {
		return "", fmt.Errorf("could not describe a subnet: %w", err)
	}

	if result == nil {
		return "", fmt.Errorf("resulting subnet is not expected to be nil")
	}

	if len(result.Subnets) > 0 {
		availabilityZone := aws.StringValue(result.Subnets[0].AvailabilityZone)
		return availabilityZone, nil
	}

	return "", fmt.Errorf("could not get an availability zone from a subnet id")
}

// getAvalabilityZoneTypeFromZoneName gets an availability zone type from specified zone name.
func getAvalabilityZoneTypeFromZoneName(zoneName string, client awsclient.Client) (string, error) {

	result, err := client.DescribeAvailabilityZones(&ec2.DescribeAvailabilityZonesInput{
		DryRun:    aws.Bool(false),
		ZoneNames: []*string{aws.String(zoneName)},
	})
	if err != nil {
		return "", fmt.Errorf("could not describe a zones: %w", err)
	}

	if result == nil {
		return "", fmt.Errorf("resulting zones is not expected to be nil")
	}

	if len(result.AvailabilityZones) > 0 {
		return aws.StringValue(result.AvailabilityZones[0].ZoneType), nil
	}

	return "", fmt.Errorf("could not get an availability zone type from a zone name")
}

func getAMI(machine runtimeclient.ObjectKey, AMI machinev1beta1.AWSResourceReference, client awsclient.Client) (*string, error) {
	if AMI.ID != nil {
		amiID := AMI.ID
		klog.Infof("Using AMI %s", *amiID)
		return amiID, nil
	}
	if len(AMI.Filters) > 0 {
		klog.Info("Describing AMI based on filters")
		describeImagesRequest := ec2.DescribeImagesInput{
			Filters: buildEC2Filters(AMI.Filters),
		}
		describeAMIResult, err := client.DescribeImages(&describeImagesRequest)
		if err != nil {
			metrics.RegisterFailedInstanceCreate(&metrics.MachineLabels{
				Name:      machine.Name,
				Namespace: machine.Namespace,
				Reason:    "error describing AMI",
			})
			klog.Errorf("error describing AMI: %v", err)
			return nil, fmt.Errorf("error describing AMI: %v", err)
		}
		if len(describeAMIResult.Images) < 1 {
			klog.Errorf("no image for given filters not found")
			return nil, fmt.Errorf("no image for given filters not found")
		}
		latestImage := describeAMIResult.Images[0]
		latestTime, err := time.Parse(time.RFC3339, *latestImage.CreationDate)
		if err != nil {
			klog.Errorf("unable to parse time for %q AMI: %v", *latestImage.ImageId, err)
			return nil, fmt.Errorf("unable to parse time for %q AMI: %v", *latestImage.ImageId, err)
		}
		for _, image := range describeAMIResult.Images[1:] {
			imageTime, err := time.Parse(time.RFC3339, *image.CreationDate)
			if err != nil {
				klog.Errorf("unable to parse time for %q AMI: %v", *image.ImageId, err)
				return nil, fmt.Errorf("unable to parse time for %q AMI: %v", *image.ImageId, err)
			}
			if latestTime.Before(imageTime) {
				latestImage = image
				latestTime = imageTime
			}
		}
		return latestImage.ImageId, nil
	}
	return nil, fmt.Errorf("AMI ID or AMI filters need to be specified")
}

func getBlockDeviceMappings(machine runtimeclient.ObjectKey, blockDeviceMappingSpecs []machinev1beta1.BlockDeviceMappingSpec, AMI string, client awsclient.Client) ([]*ec2.BlockDeviceMapping, error) {
	blockDeviceMappings := make([]*ec2.BlockDeviceMapping, 0)

	if len(blockDeviceMappingSpecs) == 0 {
		return blockDeviceMappings, nil
	}

	// Get image object to get the RootDeviceName
	describeImagesRequest := ec2.DescribeImagesInput{
		ImageIds: []*string{&AMI},
	}
	describeAMIResult, err := client.DescribeImages(&describeImagesRequest)
	if err != nil {
		metrics.RegisterFailedInstanceCreate(&metrics.MachineLabels{
			Name:      machine.Name,
			Namespace: machine.Namespace,
			Reason:    "error describing AMI",
		})
		klog.Errorf("Error describing AMI: %v", err)
		return nil, fmt.Errorf("error describing AMI: %v", err)
	}
	if len(describeAMIResult.Images) < 1 {
		klog.Errorf("No image for given AMI was found")
		return nil, fmt.Errorf("no image for given AMI not found")
	}

	rootDeviceFound := false
	for _, blockDeviceMappingSpec := range blockDeviceMappingSpecs {
		if blockDeviceMappingSpec.EBS == nil {
			continue
		}

		deviceName := blockDeviceMappingSpec.DeviceName
		volumeSize := blockDeviceMappingSpec.EBS.VolumeSize
		volumeType := blockDeviceMappingSpec.EBS.VolumeType
		deleteOnTermination := true

		if blockDeviceMappingSpec.DeviceName == nil {
			if rootDeviceFound {
				return nil, errors.New("non root device must have name")
			}
			rootDeviceFound = true
			deviceName = describeAMIResult.Images[0].RootDeviceName
		}

		blockDeviceMapping := ec2.BlockDeviceMapping{
			DeviceName: deviceName,
			Ebs: &ec2.EbsBlockDevice{
				VolumeSize:          volumeSize,
				VolumeType:          volumeType,
				Encrypted:           blockDeviceMappingSpec.EBS.Encrypted,
				DeleteOnTermination: &deleteOnTermination,
			},
		}

		// IOPS settings are only valid on IO1, IO2 and GP3 block devices
		// https://awscli.amazonaws.com/v2/documentation/api/latest/reference/ec2/create-volume.html
		switch aws.StringValue(volumeType) {
		case ec2.VolumeTypeIo1, ec2.VolumeTypeIo2, ec2.VolumeTypeGp3:
			// The installer will explicitly set the Iops to 0 if the user doesn't specify the option.
			// This means that any existing installation may break unless we ignore 0 values.
			// 0 Iops is below the minimum so AWS will fail the instance create request if we send a 0 value.
			if blockDeviceMappingSpec.EBS.Iops != nil && *blockDeviceMappingSpec.EBS.Iops > 0 {
				blockDeviceMapping.Ebs.Iops = blockDeviceMappingSpec.EBS.Iops
			}
		}

		if aws.StringValue(blockDeviceMappingSpec.EBS.KMSKey.ID) != "" {
			klog.V(3).Infof("Using KMS key ID %q for encrypting EBS volume", *blockDeviceMappingSpec.EBS.KMSKey.ID)
			blockDeviceMapping.Ebs.KmsKeyId = blockDeviceMappingSpec.EBS.KMSKey.ID
		} else if aws.StringValue(blockDeviceMappingSpec.EBS.KMSKey.ARN) != "" {
			klog.V(3).Info("Using KMS key ARN for encrypting EBS volume") // ARN usually have account ids, therefore are sensitive data so shouldn't log the value
			blockDeviceMapping.Ebs.KmsKeyId = blockDeviceMappingSpec.EBS.KMSKey.ARN
		}

		blockDeviceMappings = append(blockDeviceMappings, &blockDeviceMapping)
	}

	return blockDeviceMappings, nil
}

func launchInstance(machine *machinev1beta1.Machine, machineProviderConfig *machinev1beta1.AWSMachineProviderConfig, userData []byte, awsClient awsclient.Client, client runtimeclient.Client, infra *configv1.Infrastructure) (*ec2.Instance, error) {
	machineKey := runtimeclient.ObjectKey{
		Name:      machine.Name,
		Namespace: machine.Namespace,
	}
	amiID, err := getAMI(machineKey, machineProviderConfig.AMI, awsClient)
	if err != nil {
		return nil, mapierrors.InvalidMachineConfiguration("error getting AMI: %v", err)
	}

	securityGroupsIDs, err := getSecurityGroupsIDs(machineProviderConfig.SecurityGroups, awsClient)
	if err != nil {
		return nil, mapierrors.InvalidMachineConfiguration("error getting security groups IDs: %v", err)
	}
	subnetIDs, err := getSubnetIDs(machineKey, machineProviderConfig.Subnet, machineProviderConfig.Placement.AvailabilityZone, awsClient)
	if err != nil {
		return nil, mapierrors.InvalidMachineConfiguration("error getting subnet IDs: %v", err)
	}
	if len(subnetIDs) > 1 {
		klog.Warningf("More than one subnet id returned, only first one will be used")
	}

	// build list of networkInterfaces (just 1 for now)
	subnetID := subnetIDs[0]
	var networkInterfaces = []*ec2.InstanceNetworkInterfaceSpecification{
		{
			DeviceIndex: aws.Int64(machineProviderConfig.DeviceIndex),
			SubnetId:    subnetID,
			Groups:      securityGroupsIDs,
		},
	}

	// Public IP address assignment to instances created in Wavelength
	// Zones' subnet requires the attribute AssociateCarrierIpAddress
	// instead of AssociatePublicIpAddress.
	// AssociatePublicIpAddress and AssociateCarrierIpAddress are mutually exclusive.
	if machineProviderConfig.PublicIP != nil {
		zoneName, err := getAvalabilityZoneFromSubnetID(*subnetID, awsClient)
		if err != nil {
			return nil, mapierrors.InvalidMachineConfiguration("error discoverying zone type: %v", err)
		}
		zoneType, err := getAvalabilityZoneTypeFromZoneName(zoneName, awsClient)
		if err != nil {
			return nil, mapierrors.InvalidMachineConfiguration("error discoverying zone type: %v", err)
		}

		if zoneType == zoneTypeWavelengthZone {
			networkInterfaces[0].AssociateCarrierIpAddress = machineProviderConfig.PublicIP
		} else {
			networkInterfaces[0].AssociatePublicIpAddress = machineProviderConfig.PublicIP
		}
	}

	switch machineProviderConfig.NetworkInterfaceType {
	case machinev1beta1.AWSENANetworkInterfaceType:
		networkInterfaces[0].InterfaceType = aws.String("interface")
	case machinev1beta1.AWSEFANetworkInterfaceType:
		networkInterfaces[0].InterfaceType = aws.String("efa")
	case "":
		// If the user did not specify the interface type, do nothing
		// and let AWS use the default interface type
	default:
		return nil, mapierrors.InvalidMachineConfiguration("invalid value for networkInterfaceType %q, valid values are \"\", \"ENA\" and \"EFA\"", machineProviderConfig.NetworkInterfaceType)
	}

	blockDeviceMappings, err := getBlockDeviceMappings(machineKey, machineProviderConfig.BlockDevices, *amiID, awsClient)
	if err != nil {
		return nil, mapierrors.InvalidMachineConfiguration("error getting blockDeviceMappings: %v", err)
	}

	clusterID, ok := getClusterID(machine)
	if !ok {
		klog.Errorf("Unable to get cluster ID for machine: %q", machine.Name)
		return nil, mapierrors.InvalidMachineConfiguration("Unable to get cluster ID for machine: %q", machine.Name)
	}
	// Add tags to the created machine
	tagList := buildTagList(machine.Name, clusterID, machineProviderConfig.Tags, infra)

	tagInstance := &ec2.TagSpecification{
		ResourceType: aws.String("instance"),
		Tags:         tagList,
	}
	tagVolume := &ec2.TagSpecification{
		ResourceType: aws.String("volume"),
		Tags:         tagList,
	}

	userDataEnc := base64.StdEncoding.EncodeToString(userData)

	var iamInstanceProfile *ec2.IamInstanceProfileSpecification
	if machineProviderConfig.IAMInstanceProfile != nil && machineProviderConfig.IAMInstanceProfile.ID != nil {
		iamInstanceProfile = &ec2.IamInstanceProfileSpecification{
			Name: aws.String(*machineProviderConfig.IAMInstanceProfile.ID),
		}
	}

	placement, err := constructInstancePlacement(machine, machineProviderConfig, client)
	if err != nil {
		return nil, err
	}
	capacityReservationSpecification, err := getCapacityReservationSpecification(machineProviderConfig.CapacityReservationID)

	if err != nil {
		return nil, err
	}

	instanceMarketOptions, err := getInstanceMarketOptionsRequest(machineProviderConfig)

	if err != nil {
		return nil, err
	}

	inputConfig := ec2.RunInstancesInput{
		ImageId:      amiID,
		InstanceType: aws.String(machineProviderConfig.InstanceType),
		// Only a single instance of the AWS instance allowed
		MinCount:                         aws.Int64(1),
		MaxCount:                         aws.Int64(1),
		KeyName:                          machineProviderConfig.KeyName,
		IamInstanceProfile:               iamInstanceProfile,
		TagSpecifications:                []*ec2.TagSpecification{tagInstance, tagVolume},
		NetworkInterfaces:                networkInterfaces,
		UserData:                         &userDataEnc,
		Placement:                        placement,
		MetadataOptions:                  getInstanceMetadataOptionsRequest(machineProviderConfig),
		InstanceMarketOptions:            instanceMarketOptions,
		CapacityReservationSpecification: capacityReservationSpecification,
	}

	if len(blockDeviceMappings) > 0 {
		inputConfig.BlockDeviceMappings = blockDeviceMappings
	}
	runResult, err := awsClient.RunInstances(&inputConfig)
	if err != nil {
		metrics.RegisterFailedInstanceCreate(&metrics.MachineLabels{
			Name:      machine.Name,
			Namespace: machine.Namespace,
			Reason:    "error creating EC2 instance",
		})
		// we return InvalidMachineConfiguration for 4xx errors which by convention signal client misconfiguration
		// https://tools.ietf.org/html/rfc2616#section-6.1.1
		// https: //docs.aws.amazon.com/AWSEC2/latest/APIReference/errors-overview.html
		// https://docs.aws.amazon.com/sdk-for-go/api/aws/awserr/
		if _, ok := err.(awserr.Error); ok {
			if reqErr, ok := err.(awserr.RequestFailure); ok {
				if strings.HasPrefix(strconv.Itoa(reqErr.StatusCode()), "4") {
					klog.Errorf("Error launching instance: %v", reqErr)
					return nil, mapierrors.InvalidMachineConfiguration("error launching instance: %v", reqErr.Message())
				}
			}
		}
		klog.Errorf("Error creating EC2 instance: %v", err)
		return nil, mapierrors.CreateMachine("error creating EC2 instance: %v", err)
	}

	if runResult == nil || len(runResult.Instances) != 1 {
		klog.Errorf("Unexpected reservation creating instances: %v", runResult)
		return nil, mapierrors.CreateMachine("unexpected reservation creating instance")
	}

	return runResult.Instances[0], nil
}

// buildTagList compile a list of ec2 tags from machine provider spec and infrastructure object platform spec
func buildTagList(machineName string, clusterID string, machineTags []machinev1beta1.TagSpecification, infra *configv1.Infrastructure) []*ec2.Tag {
	rawTagList := []*ec2.Tag{}

	mergedTags := mergeInfrastructureAndMachineSpecTags(machineTags, infra)

	for _, tag := range mergedTags {
		// AWS tags are case sensitive, so we don't need to worry about other casing of "Name"
		if !strings.HasPrefix(tag.Name, "kubernetes.io/cluster/") && tag.Name != "Name" {
			rawTagList = append(rawTagList, &ec2.Tag{Key: aws.String(tag.Name), Value: aws.String(tag.Value)})
		}
	}
	rawTagList = append(rawTagList, []*ec2.Tag{
		{Key: aws.String("kubernetes.io/cluster/" + clusterID), Value: aws.String("owned")},
		{Key: aws.String("Name"), Value: aws.String(machineName)},
	}...)

	return removeDuplicatedTags(rawTagList)
}

// mergeInfrastructureAndMachineSpecTags merge list of tags from machine provider spec and Infrastructure object platform spec.
// Machine tags have precedence over Infrastructure
func mergeInfrastructureAndMachineSpecTags(machineSpecTags []machinev1beta1.TagSpecification, infra *configv1.Infrastructure) []machinev1beta1.TagSpecification {
	if infra == nil || infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || infra.Status.PlatformStatus.AWS.ResourceTags == nil {
		return machineSpecTags
	}

	mergedList := []machinev1beta1.TagSpecification{}
	mergedList = append(mergedList, machineSpecTags...)

	for _, tag := range infra.Status.PlatformStatus.AWS.ResourceTags {
		mergedList = append(mergedList, machinev1beta1.TagSpecification{Name: tag.Key, Value: tag.Value})
	}

	return mergedList
}

type instanceList []*ec2.Instance

func (il instanceList) Len() int {
	return len(il)
}

func (il instanceList) Swap(i, j int) {
	il[i], il[j] = il[j], il[i]
}

func (il instanceList) Less(i, j int) bool {
	if il[i].LaunchTime == nil && il[j].LaunchTime == nil {
		return false
	}
	if il[i].LaunchTime != nil && il[j].LaunchTime == nil {
		return false
	}
	if il[i].LaunchTime == nil && il[j].LaunchTime != nil {
		return true
	}
	return (*il[i].LaunchTime).After(*il[j].LaunchTime)
}

// sortInstances will sort a list of instance based on an instace launch time
// from the newest to the oldest.
// This function should only be called with running instances, not those which are stopped or
// terminated.
func sortInstances(instances []*ec2.Instance) {
	sort.Sort(instanceList(instances))
}

func getInstanceMarketOptionsRequest(providerConfig *machinev1beta1.AWSMachineProviderConfig) (*ec2.InstanceMarketOptionsRequest, error) {
	if providerConfig.MarketType != "" && providerConfig.MarketType == machinev1beta1.MarketTypeCapacityBlock && providerConfig.SpotMarketOptions != nil {
		return nil, errors.New("can't create spot capacity-blocks, remove spot market request")
	}

	// Infer MarketType if not explicitly set
	if providerConfig.SpotMarketOptions != nil && providerConfig.MarketType == "" {
		providerConfig.MarketType = machinev1beta1.MarketTypeSpot
	}

	if providerConfig.MarketType == "" {
		providerConfig.MarketType = machinev1beta1.MarketTypeOnDemand
	}

	switch providerConfig.MarketType {
	case machinev1beta1.MarketTypeCapacityBlock:
		if providerConfig.CapacityReservationID == "" {
			return nil, errors.New("capacityReservationID is required when CapacityBlock is enabled")
		}
		return &ec2.InstanceMarketOptionsRequest{
			MarketType: aws.String(ec2.MarketTypeCapacityBlock),
		}, nil
	case machinev1beta1.MarketTypeSpot:
		// Set required values for Spot instances
		spotOpts := &ec2.SpotMarketOptions{
			// The following two options ensure that:
			// - If an instance is interrupted, it is terminated rather than hibernating or stopping
			// - No replacement instance will be created if the instance is interrupted
			// - If the spot request cannot immediately be fulfilled, it will not be created
			// This behaviour should satisfy the 1:1 mapping of Machines to Instances as
			// assumed by the Cluster API.
			InstanceInterruptionBehavior: aws.String(ec2.InstanceInterruptionBehaviorTerminate),
			SpotInstanceType:             aws.String(ec2.SpotInstanceTypeOneTime),
		}

		if maxPrice := aws.StringValue(providerConfig.SpotMarketOptions.MaxPrice); maxPrice != "" {
			spotOpts.MaxPrice = aws.String(maxPrice)
		}

		return &ec2.InstanceMarketOptionsRequest{
			MarketType:  aws.String(ec2.MarketTypeSpot),
			SpotOptions: spotOpts,
		}, nil
	case machinev1beta1.MarketTypeOnDemand:
		// Instance is on-demand or empty
		return nil, nil
	default:
		// Invalid MarketType provided
		return nil, fmt.Errorf("invalid MarketType %q", providerConfig.MarketType)
	}
}

// constructInstancePlacement configures the placement options for the RunInstances request
func constructInstancePlacement(machine *machinev1beta1.Machine, machineProviderConfig *machinev1beta1.AWSMachineProviderConfig, client runtimeclient.Client) (*ec2.Placement, error) {
	placement := &ec2.Placement{}
	if machineProviderConfig.Placement.AvailabilityZone != "" && machineProviderConfig.Subnet.ID == nil {
		placement.SetAvailabilityZone(machineProviderConfig.Placement.AvailabilityZone)
	}

	if machineProviderConfig.PlacementGroupName != "" {
		placement.GroupName = &machineProviderConfig.PlacementGroupName

		if machineProviderConfig.PlacementGroupPartition != nil {
			placement.PartitionNumber = aws.Int64(int64(*machineProviderConfig.PlacementGroupPartition))
		}
	}

	instanceTenancy := machineProviderConfig.Placement.Tenancy
	switch instanceTenancy {
	case "":
		// Do nothing when not set
	case machinev1beta1.DefaultTenancy, machinev1beta1.DedicatedTenancy, machinev1beta1.HostTenancy:
		placement.SetTenancy(string(instanceTenancy))
	default:
		return nil, mapierrors.InvalidMachineConfiguration("invalid instance tenancy: %s. Allowed options are: %s,%s,%s",
			instanceTenancy,
			machinev1beta1.DefaultTenancy,
			machinev1beta1.DedicatedTenancy,
			machinev1beta1.HostTenancy)
	}

	if *placement == (ec2.Placement{}) {
		// If the placement is empty, we should just return a nil so as not to pollute the RunInstancesInput
		return nil, nil
	}

	return placement, nil
}

func getInstanceMetadataOptionsRequest(providerConfig *machinev1beta1.AWSMachineProviderConfig) *ec2.InstanceMetadataOptionsRequest {
	imdsOptions := &ec2.InstanceMetadataOptionsRequest{}

	switch providerConfig.MetadataServiceOptions.Authentication {
	case "":
		// not set, let aws to pick a default. `optional` at this point.
		// https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_InstanceMetadataOptionsRequest.html
	case machinev1beta1.MetadataServiceAuthenticationOptional:
		imdsOptions.HttpTokens = aws.String(ec2.HttpTokensStateOptional)
	case machinev1beta1.MetadataServiceAuthenticationRequired:
		imdsOptions.HttpTokens = aws.String(ec2.HttpTokensStateRequired)
	}

	if *imdsOptions == (ec2.InstanceMetadataOptionsRequest{}) {
		// return nil instead of empty struct if there is no options set
		return nil
	}
	return imdsOptions
}

func getCapacityReservationSpecification(capacityReservationID string) (*ec2.CapacityReservationSpecification, error) {
	if capacityReservationID == "" {
		//  Not targeting any specific Capacity Reservation
		return nil, nil
	}

	// Starts with cr-xxxxxxxxxxxxxxxxx with length of 17 characters excluding cr-
	re := regexp.MustCompile(`^cr-[0-9a-f]{17}$`)

	if !re.MatchString(capacityReservationID) {
		// It must starts with cr-xxxxxxxxxxxxxxxxx with length of 17 characters excluding cr-
		return nil, mapierrors.InvalidMachineConfiguration("Invalid value for capacityReservationId: %q, it must start with 'cr-' and be exactly 20 characters long with 17 hexadecimal characters.", capacityReservationID)
	}

	return &ec2.CapacityReservationSpecification{
		CapacityReservationTarget: &ec2.CapacityReservationTarget{
			CapacityReservationId: aws.String(capacityReservationID),
		},
	}, nil
}
