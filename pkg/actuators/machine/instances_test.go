package machine

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	gmg "github.com/onsi/gomega"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	mockaws "github.com/openshift/machine-api-provider-aws/pkg/client/mock"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRemoveDuplicatedTags(t *testing.T) {
	cases := []struct {
		tagList  []*ec2.Tag
		expected []*ec2.Tag
	}{
		{
			// empty tags
			tagList:  []*ec2.Tag{},
			expected: []*ec2.Tag{},
		},
		{
			// no duplicate tags
			tagList: []*ec2.Tag{
				{Key: aws.String("clusterID"), Value: aws.String("test-ClusterIDValue")},
			},
			expected: []*ec2.Tag{
				{Key: aws.String("clusterID"), Value: aws.String("test-ClusterIDValue")},
			},
		},
		{
			// multiple duplicate tags
			tagList: []*ec2.Tag{
				{Key: aws.String("clusterID"), Value: aws.String("test-ClusterIDValue")},
				{Key: aws.String("clusterSize"), Value: aws.String("test-ClusterSizeValue")},
				{Key: aws.String("clusterSize"), Value: aws.String("test-ClusterSizeDuplicatedValue")},
			},
			expected: []*ec2.Tag{
				{Key: aws.String("clusterID"), Value: aws.String("test-ClusterIDValue")},
				{Key: aws.String("clusterSize"), Value: aws.String("test-ClusterSizeValue")},
			},
		},
	}

	for i, c := range cases {
		actual := removeDuplicatedTags(c.tagList)
		if !reflect.DeepEqual(c.expected, actual) {
			t.Errorf("test #%d: expected %+v, got %+v", i, c.expected, actual)
		}
	}
}

func TestBuildTagList(t *testing.T) {
	cases := []struct {
		name            string
		machineSpecTags []machinev1beta1.TagSpecification
		infra           *configv1.Infrastructure
		expected        []*ec2.Tag
	}{
		{
			name:            "with empty infra and provider spec should return default tags",
			machineSpecTags: []machinev1beta1.TagSpecification{},
			infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						AWS: &configv1.AWSPlatformStatus{
							ResourceTags: []configv1.AWSResourceTag{},
						},
					},
				},
			},
			expected: []*ec2.Tag{
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
		{
			name:            "with empty infra should return default tags",
			machineSpecTags: []machinev1beta1.TagSpecification{},
			infra:           &configv1.Infrastructure{}, // should work with empty infra object
			expected: []*ec2.Tag{
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
		{
			name:            "with nil infra should  return default tags",
			machineSpecTags: []machinev1beta1.TagSpecification{},
			infra:           nil, // should work with nil infra object
			expected: []*ec2.Tag{
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
		{
			name: "should filter out bad tags from provider spec",
			machineSpecTags: []machinev1beta1.TagSpecification{
				{Name: "Name", Value: "badname"},
				{Name: "kubernetes.io/cluster/badid", Value: "badvalue"},
				{Name: "good", Value: "goodvalue"},
			},
			infra: nil,
			// Invalid tags get dropped and the valid clusterID and Name get applied last.
			expected: []*ec2.Tag{
				{Key: aws.String("good"), Value: aws.String("goodvalue")},
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
		{
			name:            "should filter out bad tags from infra object",
			machineSpecTags: []machinev1beta1.TagSpecification{},
			infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						AWS: &configv1.AWSPlatformStatus{
							ResourceTags: []configv1.AWSResourceTag{
								{
									Key:   "kubernetes.io/cluster/badid",
									Value: "badvalue",
								},
								{
									Key:   "Name",
									Value: "badname",
								},
								{
									Key:   "good",
									Value: "goodvalue",
								},
							},
						},
					},
				},
			},
			// Invalid tags get dropped and the valid clusterID and Name get applied last.
			expected: []*ec2.Tag{
				{Key: aws.String("good"), Value: aws.String("goodvalue")},
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
		{
			name: "tags from machine object should have precedence",
			machineSpecTags: []machinev1beta1.TagSpecification{
				{Name: "Name", Value: "badname"},
				{Name: "kubernetes.io/cluster/badid", Value: "badvalue"},
				{Name: "good", Value: "goodvalue"},
			},
			infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						AWS: &configv1.AWSPlatformStatus{
							ResourceTags: []configv1.AWSResourceTag{
								{
									Key:   "good",
									Value: "should-be-overwritten",
								},
							},
						},
					},
				},
			},
			expected: []*ec2.Tag{
				{Key: aws.String("good"), Value: aws.String("goodvalue")},
				{Key: aws.String("kubernetes.io/cluster/clusterID"), Value: aws.String("owned")},
				{Key: aws.String("Name"), Value: aws.String("machineName")},
			},
		},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actual := buildTagList("machineName", "clusterID", c.machineSpecTags, c.infra)
			if !reflect.DeepEqual(c.expected, actual) {
				t.Errorf("test #%d: expected %+v, got %+v", i, c.expected, actual)
			}
		})
	}
}

func TestBuildEC2Filters(t *testing.T) {
	filter1 := "filter1"
	filter2 := "filter2"
	value1 := "A"
	value2 := "B"
	value3 := "C"

	inputFilters := []machinev1beta1.Filter{
		{
			Name:   filter1,
			Values: []string{value1, value2},
		},
		{
			Name:   filter2,
			Values: []string{value3},
		},
	}

	expected := []*ec2.Filter{
		{
			Name:   &filter1,
			Values: []*string{&value1, &value2},
		},
		{
			Name:   &filter2,
			Values: []*string{&value3},
		},
	}

	got := buildEC2Filters(inputFilters)
	if !reflect.DeepEqual(expected, got) {
		t.Errorf("failed to buildEC2Filters. Expected: %+v, got: %+v", expected, got)
	}
}

func TestGetBlockDeviceMappings(t *testing.T) {
	rootDeviceName := "/dev/sda1"
	volumeSize := int64(16384)
	deviceName2 := "/dev/sda2"
	volumeSize2 := int64(16385)
	deleteOnTermination := true
	volumeType := "ssd"

	mockCtrl := gomock.NewController(t)
	mockAWSClient := mockaws.NewMockClient(mockCtrl)
	mockAWSClient.EXPECT().DescribeImages(gomock.Any()).Return(&ec2.DescribeImagesOutput{
		Images: []*ec2.Image{
			{
				CreationDate:   aws.String(time.RFC3339),
				ImageId:        aws.String("ami-1111"),
				RootDeviceName: &rootDeviceName,
			},
		},
	}, nil).AnyTimes()

	oneBlockDevice := []machinev1beta1.BlockDeviceMappingSpec{
		{
			DeviceName: &rootDeviceName,
			EBS: &machinev1beta1.EBSBlockDeviceSpec{
				VolumeSize: &volumeSize,
				VolumeType: &volumeType,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
	}

	oneExpectedBlockDevice := []*ec2.BlockDeviceMapping{
		{
			DeviceName: &rootDeviceName,
			Ebs: &ec2.EbsBlockDevice{
				VolumeSize:          &volumeSize,
				VolumeType:          &volumeType,
				DeleteOnTermination: &deleteOnTermination,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
	}

	blockDevices := []machinev1beta1.BlockDeviceMappingSpec{
		{
			DeviceName: &rootDeviceName,
			EBS: &machinev1beta1.EBSBlockDeviceSpec{
				VolumeSize: &volumeSize,
				VolumeType: &volumeType,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
		{
			DeviceName: &deviceName2,
			EBS: &machinev1beta1.EBSBlockDeviceSpec{
				VolumeSize: &volumeSize2,
				VolumeType: &volumeType,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
	}

	twoExpectedDevices := []*ec2.BlockDeviceMapping{
		{
			DeviceName: &rootDeviceName,
			Ebs: &ec2.EbsBlockDevice{
				VolumeSize:          &volumeSize,
				VolumeType:          &volumeType,
				DeleteOnTermination: &deleteOnTermination,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
		{
			DeviceName: &deviceName2,
			Ebs: &ec2.EbsBlockDevice{
				VolumeSize:          &volumeSize2,
				VolumeType:          &volumeType,
				DeleteOnTermination: &deleteOnTermination,
			},
			NoDevice:    nil,
			VirtualName: nil,
		},
	}

	blockDevicesOneEmptyName := make([]machinev1beta1.BlockDeviceMappingSpec, len(blockDevices))
	copy(blockDevicesOneEmptyName, blockDevices)
	blockDevicesOneEmptyName[0].DeviceName = nil

	blockDevicesTwoEmptyNames := make([]machinev1beta1.BlockDeviceMappingSpec, len(blockDevicesOneEmptyName))
	copy(blockDevicesTwoEmptyNames, blockDevicesOneEmptyName)
	blockDevicesTwoEmptyNames[1].DeviceName = nil

	testCases := []struct {
		description  string
		blockDevices []machinev1beta1.BlockDeviceMappingSpec
		expected     []*ec2.BlockDeviceMapping
		expectedErr  bool
	}{
		{
			description:  "When it gets an empty blockDevices list",
			blockDevices: []machinev1beta1.BlockDeviceMappingSpec{},
			expected:     []*ec2.BlockDeviceMapping{},
		},
		{
			description:  "When it gets one blockDevice",
			blockDevices: oneBlockDevice,
			expected:     oneExpectedBlockDevice,
		},
		{
			description:  "When it gets two blockDevices",
			blockDevices: blockDevices,
			expected:     twoExpectedDevices,
		},
		{
			description:  "When it gets two blockDevices and one with empty device name",
			blockDevices: blockDevicesOneEmptyName,
			expected:     twoExpectedDevices,
		},
		{
			description:  "Fail when it gets two blockDevices and two with empty device name",
			blockDevices: blockDevicesTwoEmptyNames,
			expectedErr:  true,
		},
	}

	fakeMachineKey := client.ObjectKey{
		Name:      "fake",
		Namespace: "fake",
	}
	for _, tc := range testCases {
		got, err := getBlockDeviceMappings(fakeMachineKey, tc.blockDevices, "existing-AMI", mockAWSClient)
		if tc.expectedErr {
			if err == nil {
				t.Error("Expected error")
			}
		} else {
			if err != nil {
				t.Errorf("error when calling getBlockDeviceMappings: %v", err)
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("Got: %v, expected: %v", got, tc.expected)
			}
		}
	}
}

func TestRemoveStoppedMachine(t *testing.T) {
	machine, err := stubMachine()
	if err != nil {
		t.Fatalf("Unable to build test machine manifest: %v", err)
	}

	cases := []struct {
		name   string
		output *ec2.DescribeInstancesOutput
		err    error
	}{
		{
			name:   "DescribeInstances with error",
			output: &ec2.DescribeInstancesOutput{},
			// any non-nil error will do
			err: fmt.Errorf("error describing instances"),
		},
		{
			name: "No instances to stop",
			output: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					{
						Instances: []*ec2.Instance{},
					},
				},
			},
		},
		{
			name: "One instance to stop",
			output: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					{
						Instances: []*ec2.Instance{
							stubInstance(stubAMIID, stubInstanceID, true),
						},
					},
				},
			},
		},
		{
			name: "Two instances to stop",
			output: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					{
						Instances: []*ec2.Instance{
							stubInstance(stubAMIID, stubInstanceID, true),
							stubInstance("ami-a9acbbd7", "i-02fcb933c5da7085d", true),
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			mockAWSClient := mockaws.NewMockClient(mockCtrl)
			// Not here to check how many times all the mocked methods get called.
			// Rather to provide fake outputs to get through all possible execution paths.
			mockAWSClient.EXPECT().DescribeInstances(gomock.Any()).Return(tc.output, tc.err).AnyTimes()
			mockAWSClient.EXPECT().TerminateInstances(gomock.Any()).AnyTimes()
			removeStoppedMachine(machine, mockAWSClient)
		})
	}
}

func TestLaunchInstance(t *testing.T) {
	machine, err := stubMachine()
	if err != nil {
		t.Fatalf("Unable to build test machine manifest: %v", err)
	}

	providerConfig := stubProviderConfig()
	stubTagList := buildTagList(machine.Name, stubClusterID, providerConfig.Tags, nil)

	infra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{
						{
							Key:   "infra-tag-key",
							Value: "infra-tag-value",
						},
					},
				},
			},
		},
	}

	stubTagListWithInfraObject := buildTagList(machine.Name, stubClusterID, providerConfig.Tags, infra)

	cases := []struct {
		name                string
		providerConfig      *machinev1beta1.AWSMachineProviderConfig
		securityGroupOutput *ec2.DescribeSecurityGroupsOutput
		securityGroupErr    error
		subnetOutput        *ec2.DescribeSubnetsOutput
		subnetErr           error
		zonesOutput         *ec2.DescribeAvailabilityZonesOutput
		azErr               error
		imageOutput         *ec2.DescribeImagesOutput
		imageErr            error
		instancesOutput     *ec2.Reservation
		instancesErr        error
		objects             []runtime.Object
		succeeds            bool
		runInstancesInput   *ec2.RunInstancesInput
		infra               *configv1.Infrastructure
	}{
		{
			name: "Security groups with a filter",
			providerConfig: stubPCSecurityGroups(
				[]machinev1beta1.AWSResourceReference{
					{
						Filters: []machinev1beta1.Filter{},
					},
				},
			),
			securityGroupOutput: &ec2.DescribeSecurityGroupsOutput{
				SecurityGroups: []*ec2.SecurityGroup{
					{
						GroupId: aws.String("groupID"),
					},
				},
			},
			subnetOutput:    stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   []*string{aws.String("groupID")},
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "Security groups with filters with error",
			providerConfig: stubPCSecurityGroups(
				[]machinev1beta1.AWSResourceReference{
					{
						Filters: []machinev1beta1.Filter{},
					},
				},
			),
			securityGroupErr: fmt.Errorf("error"),
		},
		{
			name: "Security groups with 2 filters",
			providerConfig: stubPCSecurityGroups(
				[]machinev1beta1.AWSResourceReference{
					{
						Filters: []machinev1beta1.Filter{},
					},
				},
			),
			securityGroupOutput: &ec2.DescribeSecurityGroupsOutput{
				SecurityGroups: []*ec2.SecurityGroup{
					{
						GroupId: aws.String("groupID1"),
					},
					{
						GroupId: aws.String("groupID2"),
					},
				},
			},
			subnetOutput:    stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   []*string{aws.String("groupID1"), aws.String("groupID2")},
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "No security group",
			providerConfig: stubPCSecurityGroups(
				[]machinev1beta1.AWSResourceReference{
					{
						Filters: []machinev1beta1.Filter{},
					},
				},
			),
			securityGroupOutput: &ec2.DescribeSecurityGroupsOutput{
				SecurityGroups: []*ec2.SecurityGroup{},
			},
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "Subnet with filters",
			providerConfig: stubPCSubnet(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{},
			}),
			subnetOutput:    stubDescribeSubnetsOutputDefault(),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 aws.String("subnetID"),
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
				Placement: &ec2.Placement{
					AvailabilityZone: aws.String("us-east-1a"),
				},
			},
		},
		{
			name: "Subnet with filters with error",
			providerConfig: stubPCSubnet(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{},
			}),
			subnetErr: fmt.Errorf("error"),
		},
		{
			name: "Subnet with availability zone with error",
			providerConfig: stubPCSubnet(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{},
			}),
			subnetOutput: &ec2.DescribeSubnetsOutput{},
			zonesOutput:  &ec2.DescribeAvailabilityZonesOutput{},
			azErr:        fmt.Errorf("error"),
		},
		{
			name: "AMI with filters",
			providerConfig: stubPCAMI(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{
					{
						Name:   "foo",
						Values: []string{"bar"},
					},
				},
			}),
			imageOutput: &ec2.DescribeImagesOutput{
				Images: []*ec2.Image{
					{
						CreationDate: aws.String("2006-01-02T15:04:05Z"),
						ImageId:      aws.String("ami-1111"),
					},
				},
			},
			subnetOutput:    stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String("ami-1111"),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "AMI with filters with error",
			providerConfig: stubPCAMI(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{},
			}),
			imageErr: fmt.Errorf("error"),
		},
		{
			name: "AMI with filters with no image",
			providerConfig: stubPCAMI(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{
					{
						Name:   "image_stage",
						Values: []string{"base"},
					},
				},
			}),
			imageOutput: &ec2.DescribeImagesOutput{
				Images: []*ec2.Image{},
			},
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 aws.String("subnetID"),
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
				Placement: &ec2.Placement{
					AvailabilityZone: aws.String("us-east-1a"),
				},
			},
		},
		{
			name: "AMI with filters with two images",
			providerConfig: stubPCAMI(machinev1beta1.AWSResourceReference{
				Filters: []machinev1beta1.Filter{
					{
						Name:   "image_stage",
						Values: []string{"base"},
					},
				},
			}),
			imageOutput: &ec2.DescribeImagesOutput{
				Images: []*ec2.Image{
					{
						CreationDate: aws.String("2006-01-02T15:04:05Z"),
						ImageId:      aws.String("ami-1111"),
					},
					{
						CreationDate: aws.String("2006-01-02T15:04:05Z"),
						ImageId:      aws.String("ami-2222"),
					},
				},
			},
			subnetOutput:    stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String("ami-1111"),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name:           "AMI not specified",
			providerConfig: stubPCAMI(machinev1beta1.AWSResourceReference{}),
		},
		{
			name:           "Dedicated instance tenancy",
			providerConfig: stubDedicatedInstanceTenancy(),
			subnetOutput:   stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:    stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
				Placement: &ec2.Placement{
					Tenancy: aws.String("dedicated"),
				},
			},
		},
		{
			name:           "Dedicated instance tenancy",
			providerConfig: stubInvalidInstanceTenancy(),
			subnetOutput: &ec2.DescribeSubnetsOutput{
				Subnets: []*ec2.Subnet{
					stubSubnet("subnetID", defaultAvailabilityZone),
				},
			},
			zonesOutput: &ec2.DescribeAvailabilityZonesOutput{
				AvailabilityZones: []*ec2.AvailabilityZone{
					stubAvailabilityZone(defaultAvailabilityZone, "availability-zone"),
				},
			},
		},
		{
			name:           "Attach infrastructure object tags",
			providerConfig: providerConfig,
			infra:          infra,
			subnetOutput:   stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:    stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagListWithInfraObject,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagListWithInfraObject,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name:            "With an EFA Network Interface Type",
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			providerConfig:  stubEFANetworkInterfaceType(),
			succeeds:        true,
			infra:           infra,
			subnetOutput:    stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagListWithInfraObject,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagListWithInfraObject,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
						InterfaceType:            aws.String("efa"),
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name:           "With an invalid Network Interface Type",
			providerConfig: stubInvalidNetworkInterfaceType(),
			succeeds:       false,
			infra:          infra,
			subnetOutput:   stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:    stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagListWithInfraObject,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagListWithInfraObject,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
						InterfaceType:            aws.String("efa"),
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name:           "With custom placement group name",
			providerConfig: stubInstancePlacementGroupName("placement-group1"),
			subnetOutput:   stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:    stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
				Placement: &ec2.Placement{
					GroupName: aws.String("placement-group1"),
				},
			},
		},
		{
			name:           "With custom placement group name and partition number",
			providerConfig: stubInstancePlacementGroupPartition("placement-group1", 4),
			subnetOutput:   stubDescribeSubnetsOutputProvided(aws.StringValue(providerConfig.Subnet.ID)),
			zonesOutput:    stubDescribeAvailabilityZonesOutputDefault(),
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						AssociatePublicIpAddress: providerConfig.PublicIP,
						SubnetId:                 providerConfig.Subnet.ID,
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
				Placement: &ec2.Placement{
					GroupName:       aws.String("placement-group1"),
					PartitionNumber: aws.Int64(4),
				},
			},
		},
		{
			name: "Wavelength Zone with Public IP",
			providerConfig: stubProviderConfigCustomized(&stubInput{
				InstanceType: "m5d.2xlarge",
				ZoneName:     defaultWavelengthZone,
				IsPublic:     aws.Bool(true),
			}),
			subnetOutput:    stubDescribeSubnetsOutputWavelength(),
			zonesOutput:     stubDescribeAvailabilityZonesOutputWavelength(),
			instancesOutput: stubReservationEdgeZones(stubAMIID, stubInstanceID, "192.168.0.10", defaultWavelengthZone),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: aws.String("m5d.2xlarge"),
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						AssociateCarrierIpAddress: aws.Bool(true),
						DeviceIndex:               aws.Int64(providerConfig.DeviceIndex),
						SubnetId:                  aws.String(stubSubnetID),
						Groups:                    stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "Wavelength Zone with Private IP",
			providerConfig: stubProviderConfigCustomized(&stubInput{
				InstanceType: "m5d.2xlarge",
				ZoneName:     defaultWavelengthZone,
				IsPublic:     aws.Bool(false),
			}),
			subnetOutput:    stubDescribeSubnetsOutputWavelength(),
			zonesOutput:     stubDescribeAvailabilityZonesOutputWavelength(),
			instancesOutput: stubReservationEdgeZones(stubAMIID, stubInstanceID, "192.168.0.10", defaultWavelengthZone),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: aws.String("m5d.2xlarge"),
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						AssociateCarrierIpAddress: aws.Bool(false),
						DeviceIndex:               aws.Int64(providerConfig.DeviceIndex),
						SubnetId:                  aws.String(stubSubnetID),
						Groups:                    stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
		{
			name: "Regular Zone with Private IP",
			providerConfig: stubProviderConfigCustomized(&stubInput{
				ZoneName: defaultAvailabilityZone,
				IsPublic: aws.Bool(false),
			}),
			subnetOutput:    stubDescribeSubnetsOutputDefault(),
			zonesOutput:     stubDescribeAvailabilityZonesOutputDefault(),
			instancesOutput: stubReservation(stubAMIID, stubInstanceID, "192.168.0.10"),
			succeeds:        true,
			runInstancesInput: &ec2.RunInstancesInput{
				IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
					Name: aws.String(*providerConfig.IAMInstanceProfile.ID),
				},
				ImageId:      aws.String(*providerConfig.AMI.ID),
				InstanceType: &providerConfig.InstanceType,
				MinCount:     aws.Int64(1),
				MaxCount:     aws.Int64(1),
				KeyName:      providerConfig.KeyName,
				TagSpecifications: []*ec2.TagSpecification{{
					ResourceType: aws.String("instance"),
					Tags:         stubTagList,
				}, {
					ResourceType: aws.String("volume"),
					Tags:         stubTagList,
				}},
				NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
					{
						AssociatePublicIpAddress: aws.Bool(false),
						DeviceIndex:              aws.Int64(providerConfig.DeviceIndex),
						SubnetId:                 aws.String(stubSubnetID),
						Groups:                   stubSecurityGroupsDefault,
					},
				},
				UserData: aws.String(""),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			mockAWSClient := mockaws.NewMockClient(mockCtrl)

			mockAWSClient.EXPECT().DescribeSecurityGroups(gomock.Any()).Return(tc.securityGroupOutput, tc.securityGroupErr).AnyTimes()
			mockAWSClient.EXPECT().DescribeAvailabilityZones(gomock.Any()).Return(tc.zonesOutput, nil).AnyTimes()
			mockAWSClient.EXPECT().DescribeSubnets(gomock.Any()).Return(tc.subnetOutput, tc.subnetErr).AnyTimes()
			mockAWSClient.EXPECT().DescribeImages(gomock.Any()).Return(tc.imageOutput, tc.imageErr).AnyTimes()
			mockAWSClient.EXPECT().RunInstances(tc.runInstancesInput).Return(tc.instancesOutput, tc.instancesErr).AnyTimes()

			fakeClient := fake.NewFakeClient(tc.objects...)

			_, launchErr := launchInstance(machine, tc.providerConfig, nil, mockAWSClient, fakeClient, tc.infra)
			t.Log(launchErr)
			if launchErr == nil {
				if !tc.succeeds {
					t.Errorf("Call to launchInstance did not fail as expected")
				}
			} else {
				if tc.succeeds {
					t.Errorf("Call to launchInstance did not succeed as expected")
				}
			}
		})
	}
}

func TestSortInstances(t *testing.T) {
	instances := []*ec2.Instance{
		{
			LaunchTime: aws.Time(time.Now()),
		},
		{
			LaunchTime: nil,
		},
		{
			LaunchTime: nil,
		},
		{
			LaunchTime: aws.Time(time.Now()),
		},
	}
	sortInstances(instances)
}

func TestGetInstanceMarketOptionsRequest(t *testing.T) {
	mockCapacityReservationID := "cr-123"
	testCases := []struct {
		name            string
		providerConfig  *machinev1beta1.AWSMachineProviderConfig
		expectedRequest *ec2.InstanceMarketOptionsRequest
		wantErr         bool
	}{
		{
			name:            "with no Spot options specified",
			providerConfig:  &machinev1beta1.AWSMachineProviderConfig{},
			expectedRequest: nil,
			wantErr:         false,
		},
		{
			name: "with an empty Spot options specified",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				SpotMarketOptions: &machinev1beta1.SpotMarketOptions{},
			},
			expectedRequest: &ec2.InstanceMarketOptionsRequest{
				MarketType: aws.String(ec2.MarketTypeSpot),
				SpotOptions: &ec2.SpotMarketOptions{
					InstanceInterruptionBehavior: aws.String(ec2.InstanceInterruptionBehaviorTerminate),
					SpotInstanceType:             aws.String(ec2.SpotInstanceTypeOneTime),
				},
			},
			wantErr: false,
		},
		{
			name: "with an empty MaxPrice specified",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				SpotMarketOptions: &machinev1beta1.SpotMarketOptions{
					MaxPrice: aws.String(""),
				},
			},
			expectedRequest: &ec2.InstanceMarketOptionsRequest{
				MarketType: aws.String(ec2.MarketTypeSpot),
				SpotOptions: &ec2.SpotMarketOptions{
					InstanceInterruptionBehavior: aws.String(ec2.InstanceInterruptionBehaviorTerminate),
					SpotInstanceType:             aws.String(ec2.SpotInstanceTypeOneTime),
				},
			},
			wantErr: false,
		},
		{
			name: "with a valid MaxPrice specified",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				SpotMarketOptions: &machinev1beta1.SpotMarketOptions{
					MaxPrice: aws.String("0.01"),
				},
			},
			expectedRequest: &ec2.InstanceMarketOptionsRequest{
				MarketType: aws.String(ec2.MarketTypeSpot),
				SpotOptions: &ec2.SpotMarketOptions{
					InstanceInterruptionBehavior: aws.String(ec2.InstanceInterruptionBehaviorTerminate),
					SpotInstanceType:             aws.String(ec2.SpotInstanceTypeOneTime),
					MaxPrice:                     aws.String("0.01"),
				},
			},
			wantErr: false,
		},
		{
			name:            "invalid MarketType specified",
			expectedRequest: nil,
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MarketType: machinev1beta1.MarketType("invalid"),
			},
			wantErr: true,
		},
		{
			name: "with a MarketType to MarketTypeCapacityBlock specified with capacityReservationID set to nil",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MarketType:            machinev1beta1.MarketTypeCapacityBlock,
				CapacityReservationID: "",
			},
			expectedRequest: nil,
			wantErr:         true,
		},
		{
			name: "with a MarketType to MarketTypeCapacityBlock with capacityReservationID set to nil",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MarketType:            machinev1beta1.MarketTypeCapacityBlock,
				CapacityReservationID: mockCapacityReservationID,
			},
			expectedRequest: &ec2.InstanceMarketOptionsRequest{
				MarketType: aws.String(ec2.MarketTypeCapacityBlock),
			},
			wantErr: false,
		},
		{
			name: "with a MarketType to MarketTypeCapacityBlock set with capacityReservationID set and empty Spot options specified",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MarketType:            machinev1beta1.MarketTypeCapacityBlock,
				CapacityReservationID: mockCapacityReservationID,
				SpotMarketOptions:     &machinev1beta1.SpotMarketOptions{},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := gmg.NewWithT(t)

			request, err := getInstanceMarketOptionsRequest(tc.providerConfig)
			if err == nil {
				g.Expect(request).To(gmg.BeEquivalentTo(tc.expectedRequest))
			} else {
				g.Expect(err).To(gmg.HaveOccurred())
			}
		})
	}
}

func TestGetInstanceMetadataOptionsRequest(t *testing.T) {
	testCases := []struct {
		name           string
		providerConfig *machinev1beta1.AWSMachineProviderConfig
		expected       *ec2.InstanceMetadataOptionsRequest
	}{
		{
			name:           "no imds options specified",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{},
			expected:       nil,
		},
		{
			name: "imds required",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MetadataServiceOptions: machinev1beta1.MetadataServiceOptions{
					Authentication: machinev1beta1.MetadataServiceAuthenticationRequired,
				},
			},
			expected: &ec2.InstanceMetadataOptionsRequest{
				HttpTokens: aws.String(ec2.HttpTokensStateRequired),
			},
		},
		{
			name: "imds optional",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MetadataServiceOptions: machinev1beta1.MetadataServiceOptions{
					Authentication: machinev1beta1.MetadataServiceAuthenticationOptional,
				},
			},
			expected: &ec2.InstanceMetadataOptionsRequest{
				HttpTokens: aws.String(ec2.HttpTokensStateOptional),
			},
		},
		{
			// Should not happen due to resource validation during creation, just it case for ensure that doesn't blow up
			name: "crappy input",
			providerConfig: &machinev1beta1.AWSMachineProviderConfig{
				MetadataServiceOptions: machinev1beta1.MetadataServiceOptions{
					Authentication: "foooobaaaar",
				},
			},
			expected: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := gmg.NewWithT(t)
			req := getInstanceMetadataOptionsRequest(tc.providerConfig)
			g.Expect(req).To(gmg.BeEquivalentTo(tc.expected))
		})
	}
}

func TestCorrectExistingTags(t *testing.T) {
	machine, err := stubMachine()
	if err != nil {
		t.Fatalf("Unable to build test machine manifest: %v", err)
	}
	clusterID, _ := getClusterID(machine)
	instance := ec2.Instance{
		InstanceId: aws.String(stubInstanceID),
	}
	testCases := []struct {
		name               string
		tags               []*ec2.Tag
		userTags           []*ec2.Tag
		expectedCreateTags bool
	}{
		{
			name: "Valid Tags",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + clusterID),
					Value: aws.String("owned"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String(machine.Name),
				},
			},
			expectedCreateTags: false,
		},
		{
			name: "Valid Tags and Create",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + clusterID),
					Value: aws.String("owned"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String(machine.Name),
				},
			},
			expectedCreateTags: true,
			userTags: []*ec2.Tag{
				{
					Key:   aws.String("UserDefinedTag2"),
					Value: aws.String("UserDefinedTagValue2"),
				},
			},
		},
		{
			name: "Valid Tags and Update",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + clusterID),
					Value: aws.String("owned"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String(machine.Name),
				},
				{
					Key:   aws.String("UserDefinedTag1"),
					Value: aws.String("UserDefinedTagValue1"),
				},
			},
			expectedCreateTags: true,
			userTags: []*ec2.Tag{
				{
					Key:   aws.String("UserDefinedTag1"),
					Value: aws.String("ModifiedValue"),
				},
			},
		},
		{
			name: "Invalid Name Tag Correct Cluster",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + clusterID),
					Value: aws.String("owned"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String("badname"),
				},
			},
			expectedCreateTags: true,
		},
		{
			name: "Invalid Cluster Tag Correct Name",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + "badcluster"),
					Value: aws.String("owned"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String(machine.Name),
				},
			},
			expectedCreateTags: true,
		},
		{
			name: "Both Tags Wrong",
			tags: []*ec2.Tag{
				{
					Key:   aws.String("kubernetes.io/cluster/" + clusterID),
					Value: aws.String("bad value"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String("bad name"),
				},
			},
			expectedCreateTags: true,
		},
		{
			name:               "No Tags",
			tags:               nil,
			expectedCreateTags: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			// if Finish is not called, MinTimes is never enforced
			defer mockCtrl.Finish()
			mockAWSClient := mockaws.NewMockClient(mockCtrl)
			instance.Tags = tc.tags

			if tc.expectedCreateTags {
				mockAWSClient.EXPECT().CreateTags(gomock.Any()).Return(&ec2.CreateTagsOutput{}, nil).MinTimes(1)
			}

			err := correctExistingTags(machine, &instance, mockAWSClient, tc.userTags)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
		})
	}
}

func TestGetAvalabilityZoneTypeFromZoneName(t *testing.T) {
	type args struct {
		zoneName    string
		zonesOutput *ec2.DescribeAvailabilityZonesOutput
	}
	cases := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "get availability-zone",
			args: args{
				zoneName:    defaultAvailabilityZone,
				zonesOutput: stubDescribeAvailabilityZonesOutputDefault(),
			},
			want: "availability-zone",
		},
		{
			name: "get wavelength-zone",
			args: args{
				zoneName:    defaultWavelengthZone,
				zonesOutput: stubDescribeAvailabilityZonesOutputWavelength(),
			},
			want: "wavelength-zone",
		},
		{
			name: "no zone",
			args: args{
				zoneName:    defaultWavelengthZone,
				zonesOutput: &ec2.DescribeAvailabilityZonesOutput{},
			},
			wantErr: true,
		},
		{
			name: "empty input",
			args: args{
				zoneName:    "",
				zonesOutput: &ec2.DescribeAvailabilityZonesOutput{},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			mockCtrl := gomock.NewController(t)
			mockAWSClient := mockaws.NewMockClient(mockCtrl)
			mockAWSClient.EXPECT().DescribeAvailabilityZones(gomock.Any()).Return(tc.args.zonesOutput, nil).AnyTimes()

			got, err := getAvalabilityZoneTypeFromZoneName(tc.args.zoneName, mockAWSClient)
			if (err != nil) != tc.wantErr {
				t.Errorf("getAvalabilityZoneTypeFromZoneName() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("getAvalabilityZoneTypeFromZoneName() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetCapacityReservationSpecification(t *testing.T) {
	mockCapacityReservationID := "cr-1234a6789d234f6f4"
	capacityReservationIDShorterLength := "cr-1234"
	capacityReservationIDLongerLength := "cr-1234567a912345b789"
	capacityReservationIDContainsCapitalChars := "cr-B234a67891234567A"
	capacityReservationIDContainsSpecialChars := "cr-$12456789123@567A"
	testCases := []struct {
		name                  string
		capacityReservationID string
		expectedRequest       *ec2.CapacityReservationSpecification
		expectedError         error
	}{
		{
			name:                  "with no CapacityReservationID options specified",
			capacityReservationID: "",
			expectedRequest:       nil,
			expectedError:         nil,
		},
		{
			name:                  "with invalid CapacityReservationID options specified, shorter length",
			capacityReservationID: capacityReservationIDShorterLength,
			expectedRequest:       nil,
			expectedError:         mapierrors.InvalidMachineConfiguration("Invalid value for capacityReservationId: %q, it must start with 'cr-' and be exactly 20 characters long with 17 hexadecimal characters.", capacityReservationIDShorterLength),
		},
		{
			name:                  "with invalid CapacityReservationID options specified, longer length greatherthan 17",
			capacityReservationID: capacityReservationIDLongerLength,
			expectedRequest:       nil,
			expectedError:         mapierrors.InvalidMachineConfiguration("Invalid value for capacityReservationId: %q, it must start with 'cr-' and be exactly 20 characters long with 17 hexadecimal characters.", capacityReservationIDLongerLength),
		},
		{
			name:                  "with invalid CapacityReservationID options specified, contains capital",
			capacityReservationID: capacityReservationIDContainsCapitalChars,
			expectedRequest:       nil,
			expectedError:         mapierrors.InvalidMachineConfiguration("Invalid value for capacityReservationId: %q, it must start with 'cr-' and be exactly 20 characters long with 17 hexadecimal characters.", capacityReservationIDContainsCapitalChars),
		},
		{
			name:                  "with invalid CapacityReservationID options specified, contains special chars",
			capacityReservationID: capacityReservationIDContainsSpecialChars,
			expectedRequest:       nil,
			expectedError:         mapierrors.InvalidMachineConfiguration("Invalid value for capacityReservationId: %q, it must start with 'cr-' and be exactly 20 characters long with 17 hexadecimal characters.", capacityReservationIDContainsSpecialChars),
		},
		{
			name:                  "with a valid CapacityReservationID specified",
			capacityReservationID: mockCapacityReservationID,
			expectedRequest: &ec2.CapacityReservationSpecification{
				CapacityReservationTarget: &ec2.CapacityReservationTarget{
					CapacityReservationId: aws.String(mockCapacityReservationID),
				},
			},
			expectedError: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := gmg.NewWithT(t)
			req, err := getCapacityReservationSpecification(tc.capacityReservationID)
			if err == nil {
				g.Expect(req).To(gmg.BeEquivalentTo(tc.expectedRequest))
			} else {
				g.Expect(err).To(gmg.BeEquivalentTo(tc.expectedError))
			}

		})
	}
}
