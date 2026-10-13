package machine

import (
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elbv2"
	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultNamespace         = "default"
	defaultAvailabilityZone  = "us-east-1a"
	defaultWavelengthZone    = "us-east-1-wl1-nyc-wlz-1"
	defaultZoneType          = "availability-zone"
	zoneTypeWavelength       = "wavelength-zone"
	region                   = "us-east-1"
	awsCredentialsSecretName = "aws-credentials-secret"
	userDataSecretName       = "aws-actuator-user-data-secret"

	keyName         = "aws-actuator-key-name"
	stubClusterID   = "aws-actuator-cluster"
	stubMachineName = "aws-actuator-testing-machine"
	stubAMIID       = "ami-a9acbbd6"
	stubInstanceID  = "i-02fcb933c5da7085c"
	stubSubnetID    = "subnet-0e56b13a64ff8a941"
)

var stubSecurityGroupsDefault = []*string{
	aws.String("sg-00868b02fbe29de17"),
	aws.String("sg-0a4658991dc5eb40a"),
	aws.String("sg-009a70e28fa4ba84e"),
	aws.String("sg-07323d56fb932c84c"),
	aws.String("sg-08b1ffd32874d59a2"),
}

const userDataBlob = `#cloud-config
write_files:
- path: /root/node_bootstrap/node_settings.yaml
  owner: 'root:root'
  permissions: '0640'
  content: |
    node_config_name: node-config-master
runcmd:
- [ cat, /root/node_bootstrap/node_settings.yaml]
`

type stubInput struct {
	ZoneName     string
	InstanceType string
	IsPublic     *bool
}

func stubPCSecurityGroupsDefault() (groups []machinev1beta1.AWSResourceReference) {
	for _, group := range stubSecurityGroupsDefault {
		groups = append(groups, machinev1beta1.AWSResourceReference{
			ID: group,
		})
	}
	return
}

func stubProviderConfig() *machinev1beta1.AWSMachineProviderConfig {
	return &machinev1beta1.AWSMachineProviderConfig{
		AMI: machinev1beta1.AWSResourceReference{
			ID: aws.String(stubAMIID),
		},
		CredentialsSecret: &corev1.LocalObjectReference{
			Name: awsCredentialsSecretName,
		},
		InstanceType: "m4.xlarge",
		Placement: machinev1beta1.Placement{
			Region:           region,
			AvailabilityZone: defaultAvailabilityZone,
		},
		Subnet: machinev1beta1.AWSResourceReference{
			ID: aws.String("subnet-0e56b13a64ff8a941"),
		},
		IAMInstanceProfile: &machinev1beta1.AWSResourceReference{
			ID: aws.String("openshift_master_launch_instances"),
		},
		KeyName: aws.String(keyName),
		UserDataSecret: &corev1.LocalObjectReference{
			Name: userDataSecretName,
		},
		Tags: []machinev1beta1.TagSpecification{
			{Name: "openshift-node-group-config", Value: "node-config-master"},
			{Name: "host-type", Value: "master"},
			{Name: "sub-host-type", Value: "default"},
		},
		SecurityGroups: stubPCSecurityGroupsDefault(),
		PublicIP:       aws.Bool(true),
		LoadBalancers: []machinev1beta1.LoadBalancerReference{
			{
				Name: "cluster-con",
				Type: machinev1beta1.ClassicLoadBalancerType,
			},
			{
				Name: "cluster-ext",
				Type: machinev1beta1.ClassicLoadBalancerType,
			},
			{
				Name: "cluster-int",
				Type: machinev1beta1.ClassicLoadBalancerType,
			},
			{
				Name: "cluster-net-lb",
				Type: machinev1beta1.NetworkLoadBalancerType,
			},
		},
	}
}

func stubMachine() (*machinev1beta1.Machine, error) {
	machinePc := stubProviderConfig()

	providerSpec, err := RawExtensionFromProviderSpec(machinePc)
	if err != nil {
		return nil, fmt.Errorf("codec.EncodeProviderSpec failed: %v", err)
	}

	machine := &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stubMachineName,
			Namespace: defaultNamespace,
			Labels: map[string]string{
				machinev1beta1.MachineClusterIDLabel: stubClusterID,
			},
			Annotations: map[string]string{
				// skip node draining since it's not mocked
				machinecontroller.ExcludeNodeDrainingAnnotation: "",
			},
		},

		Spec: machinev1beta1.MachineSpec{
			ObjectMeta: machinev1beta1.ObjectMeta{
				Labels: map[string]string{
					"node-role.kubernetes.io/master": "",
					"node-role.kubernetes.io/infra":  "",
				},
			},
			ProviderSpec: machinev1beta1.ProviderSpec{
				Value: providerSpec,
			},
		},
	}

	return machine, nil
}

func stubUserDataSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userDataSecretName,
			Namespace: defaultNamespace,
		},
		Data: map[string][]byte{
			userDataSecretKey: []byte(userDataBlob),
		},
	}
}

func stubAwsCredentialsSecret() *corev1.Secret {
	return GenerateAwsCredentialsSecretFromEnv(awsCredentialsSecretName, defaultNamespace)
}

// GenerateAwsCredentialsSecretFromEnv generates secret with AWS credentials
func GenerateAwsCredentialsSecretFromEnv(secretName, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			awsclient.AwsCredsSecretIDKey:     []byte(os.Getenv("AWS_ACCESS_KEY_ID")),
			awsclient.AwsCredsSecretAccessKey: []byte(os.Getenv("AWS_SECRET_ACCESS_KEY")),
		},
	}
}

func stubInstance(imageID, instanceID string, setIP bool) *ec2.Instance {
	var ipAddr *string
	if setIP {
		ipAddr = aws.String("1.1.1.1")
	}
	return &ec2.Instance{
		ImageId:    aws.String(imageID),
		InstanceId: aws.String(instanceID),
		State: &ec2.InstanceState{
			Name: aws.String(ec2.InstanceStateNameRunning),
			Code: aws.Int64(16),
		},
		LaunchTime:       aws.Time(time.Now()),
		PublicDnsName:    aws.String("publicDNS"),
		PrivateDnsName:   aws.String("privateDNS"),
		PublicIpAddress:  ipAddr,
		PrivateIpAddress: ipAddr,
		Tags: []*ec2.Tag{
			{
				Key:   aws.String("key"),
				Value: aws.String("value"),
			},
		},
		IamInstanceProfile: &ec2.IamInstanceProfile{
			Id: aws.String("profile"),
		},
		SubnetId: aws.String("subnetID"),
		Placement: &ec2.Placement{
			AvailabilityZone: aws.String("us-east-1a"),
		},
		SecurityGroups: []*ec2.GroupIdentifier{
			{
				GroupName: aws.String("groupName"),
			},
		},
	}
}

func stubAvailabilityZone(zoneName, zoneType string) *ec2.AvailabilityZone {
	zName := defaultAvailabilityZone
	if zoneName != "" {
		zName = zoneName
	}
	zType := defaultZoneType
	if zoneType != "" {
		zType = zoneType
	}
	return &ec2.AvailabilityZone{
		ZoneName: aws.String(zName),
		ZoneType: aws.String(zType),
	}
}

func stubDescribeAvailabilityZonesOutputDefault() *ec2.DescribeAvailabilityZonesOutput {
	return &ec2.DescribeAvailabilityZonesOutput{
		AvailabilityZones: []*ec2.AvailabilityZone{
			stubAvailabilityZone(defaultAvailabilityZone, defaultZoneType),
		},
	}
}

func stubDescribeAvailabilityZonesOutputWavelength() *ec2.DescribeAvailabilityZonesOutput {
	return &ec2.DescribeAvailabilityZonesOutput{
		AvailabilityZones: []*ec2.AvailabilityZone{
			stubAvailabilityZone(defaultWavelengthZone, zoneTypeWavelength),
		},
	}
}

func stubDescribeAvailabilityZonesOutput() *ec2.DescribeAvailabilityZonesOutput {
	return &ec2.DescribeAvailabilityZonesOutput{
		AvailabilityZones: []*ec2.AvailabilityZone{
			stubAvailabilityZone("", ""),
		},
	}
}

func stubSubnet(subnetID, zoneName string) *ec2.Subnet {
	zName := defaultAvailabilityZone
	if zoneName != "" {
		zName = zoneName
	}
	sID := stubSubnetID
	if subnetID != "" {
		sID = subnetID
	}
	return &ec2.Subnet{
		SubnetId:         aws.String(sID),
		AvailabilityZone: aws.String(zName),
	}
}

func stubDescribeSubnetsOutput() *ec2.DescribeSubnetsOutput {
	return &ec2.DescribeSubnetsOutput{
		Subnets: []*ec2.Subnet{
			stubSubnet("", ""),
		},
	}
}

func stubDescribeSubnetsOutputDefault() *ec2.DescribeSubnetsOutput {
	return &ec2.DescribeSubnetsOutput{
		Subnets: []*ec2.Subnet{
			stubSubnet("subnetID", defaultAvailabilityZone),
		},
	}
}

func stubDescribeSubnetsOutputProvided(subnetID string) *ec2.DescribeSubnetsOutput {
	return &ec2.DescribeSubnetsOutput{
		Subnets: []*ec2.Subnet{
			stubSubnet(subnetID, defaultAvailabilityZone),
		},
	}
}

func stubDescribeSubnetsOutputWavelength() *ec2.DescribeSubnetsOutput {
	return &ec2.DescribeSubnetsOutput{
		Subnets: []*ec2.Subnet{
			stubSubnet(stubSubnetID, defaultWavelengthZone),
		},
	}
}

func stubPCSecurityGroups(groups []machinev1beta1.AWSResourceReference) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.SecurityGroups = groups
	return pc
}

func stubPCSubnet(subnet machinev1beta1.AWSResourceReference) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.Subnet = subnet
	return pc
}

func stubPCAMI(ami machinev1beta1.AWSResourceReference) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.AMI = ami
	return pc
}

func stubProviderConfigCustomized(in *stubInput) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()

	if in.InstanceType != "" {
		pc.InstanceType = in.InstanceType
	}

	if in.IsPublic != nil {
		pc.PublicIP = in.IsPublic
	}

	if in.ZoneName != "" {
		pc.Placement = machinev1beta1.Placement{
			Region:           region,
			AvailabilityZone: in.ZoneName,
		}
	}
	return pc
}

func stubDedicatedInstanceTenancy() *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.Placement.Tenancy = machinev1beta1.DedicatedTenancy
	return pc
}

func stubInstancePlacementGroupName(placementGroupName string) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.PlacementGroupName = placementGroupName
	return pc
}

func stubInstancePlacementGroupPartition(placementGroupName string, partitionNumber int32) *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.PlacementGroupName = placementGroupName
	pc.PlacementGroupPartition = &partitionNumber
	return pc
}

func stubEFANetworkInterfaceType() *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.NetworkInterfaceType = machinev1beta1.AWSEFANetworkInterfaceType
	return pc
}

func stubInvalidNetworkInterfaceType() *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.NetworkInterfaceType = "invalid"
	return pc
}

func stubInvalidInstanceTenancy() *machinev1beta1.AWSMachineProviderConfig {
	pc := stubProviderConfig()
	pc.Placement.Tenancy = "invalid"
	return pc
}

func stubDescribeLoadBalancersOutput() *elbv2.DescribeLoadBalancersOutput {
	return &elbv2.DescribeLoadBalancersOutput{
		LoadBalancers: []*elbv2.LoadBalancer{
			{
				LoadBalancerName: aws.String("lbname"),
				LoadBalancerArn:  aws.String("lbarn"),
			},
		},
	}
}

func stubDescribeTargetGroupsOutput() *elbv2.DescribeTargetGroupsOutput {
	return &elbv2.DescribeTargetGroupsOutput{
		TargetGroups: []*elbv2.TargetGroup{
			{
				TargetType:     aws.String(elbv2.TargetTypeEnumInstance),
				TargetGroupArn: aws.String("arn1"),
			},
			{
				TargetType:     aws.String(elbv2.TargetTypeEnumIp),
				TargetGroupArn: aws.String("arn2"),
			},
		},
	}
}

func stubDescribeTargetHealthOutput() *elbv2.DescribeTargetHealthOutput {
	return &elbv2.DescribeTargetHealthOutput{}
}

func stubDeregisterTargetsInput(ipAddr string) *elbv2.DeregisterTargetsInput {
	return &elbv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String("arn2"),
		Targets: []*elbv2.TargetDescription{
			{
				Id: aws.String(ipAddr),
			},
		},
	}
}

func stubReservation(imageID, instanceID string, privateIP string) *ec2.Reservation {
	az := defaultAvailabilityZone
	return &ec2.Reservation{
		Instances: []*ec2.Instance{
			{
				ImageId:    aws.String(imageID),
				InstanceId: aws.String(instanceID),
				State: &ec2.InstanceState{
					Name: aws.String(ec2.InstanceStateNamePending),
					Code: aws.Int64(16),
				},
				LaunchTime: aws.Time(time.Now()),
				Placement: &ec2.Placement{
					AvailabilityZone: &az,
				},
				PrivateIpAddress: aws.String(privateIP),
			},
		},
	}
}

func stubReservationEdgeZones(ami, iid, privateIP, zoneName string) *ec2.Reservation {
	return &ec2.Reservation{
		Instances: []*ec2.Instance{
			{
				ImageId:    aws.String(ami),
				InstanceId: aws.String(iid),
				State: &ec2.InstanceState{
					Name: aws.String(ec2.InstanceStateNamePending),
					Code: aws.Int64(16),
				},
				LaunchTime: aws.Time(time.Now()),
				Placement: &ec2.Placement{
					AvailabilityZone: aws.String(zoneName),
				},
				PrivateIpAddress: aws.String(privateIP),
			},
		},
	}
}

func stubDescribeInstancesOutput(imageID, instanceID string, state string, privateIP string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []*ec2.Reservation{
			{
				Instances: []*ec2.Instance{
					{
						ImageId:    aws.String(imageID),
						InstanceId: aws.String(instanceID),
						State: &ec2.InstanceState{
							Name: aws.String(state),
							Code: aws.Int64(16),
						},
						LaunchTime:       aws.Time(time.Now()),
						PublicIpAddress:  aws.String(privateIP),
						PrivateIpAddress: aws.String(privateIP),
						PrivateDnsName:   aws.String("privateDNS"),
						PublicDnsName:    aws.String("publicDNS"),
					},
				},
			},
		},
	}
}

func stubDescribeInstancesInput(instanceID string) *ec2.DescribeInstancesInput {
	return &ec2.DescribeInstancesInput{
		InstanceIds: aws.StringSlice([]string{instanceID}),
	}
}

func stubDescribeInstancesInputFromName() *ec2.DescribeInstancesInput {
	return &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   awsTagFilter("Name"),
				Values: aws.StringSlice([]string{stubMachineName}),
			},
			clusterFilter(stubClusterID),
		},
	}
}

// StubDescribeDHCPOptions provides fake output
func StubDescribeDHCPOptions() (*ec2.DescribeDhcpOptionsOutput, error) {
	key := "key"
	return &ec2.DescribeDhcpOptionsOutput{
		DhcpOptions: []*ec2.DhcpOptions{
			{
				DhcpConfigurations: []*ec2.DhcpConfiguration{
					{
						Key:    &key,
						Values: []*ec2.AttributeValue{},
					},
				},
			},
		},
	}, nil
}

// StubDescribeVPCs provides fake output
func StubDescribeVPCs() (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{
		Vpcs: []*ec2.Vpc{
			{
				VpcId: aws.String("vpc-32677e0e794418639"),
			},
		},
	}, nil
}

func stubInfraObject() *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: awsclient.GlobalInfrastuctureName,
		},
	}
}
