package fake

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/openshift/machine-api-provider-aws/pkg/actuators/machine"
	"github.com/openshift/machine-api-provider-aws/pkg/client"
	"k8s.io/client-go/kubernetes"
)

type awsClient struct {
}

func (c *awsClient) DescribeImages(ctx context.Context, input *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{
		Images: []ec2types.Image{
			{
				ImageId: aws.String("ami-a9acbbd6"),
			},
		},
	}, nil
}

func (c *awsClient) DescribeVpcs(ctx context.Context, input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	return machine.StubDescribeVPCs()
}

func (c *awsClient) DescribeSubnets(ctx context.Context, input *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{
		Subnets: []ec2types.Subnet{
			{
				SubnetId: aws.String("subnet-28fddb3c45cae61b5"),
			},
		},
	}, nil
}

func (c *awsClient) DescribeAvailabilityZones(ctx context.Context, input *ec2.DescribeAvailabilityZonesInput) (*ec2.DescribeAvailabilityZonesOutput, error) {
	return &ec2.DescribeAvailabilityZonesOutput{}, nil
}

func (c *awsClient) DescribeSecurityGroups(ctx context.Context, input *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{
		SecurityGroups: []ec2types.SecurityGroup{
			{
				GroupId: aws.String("sg-05acc3c38a35ce63b"),
			},
		},
	}, nil
}

func (c *awsClient) DescribePlacementGroups(ctx context.Context, input *ec2.DescribePlacementGroupsInput) (*ec2.DescribePlacementGroupsOutput, error) {
	return &ec2.DescribePlacementGroupsOutput{}, nil
}

func (c *awsClient) DescribeDHCPOptions(ctx context.Context, input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
	return machine.StubDescribeDHCPOptions()
}

func (c *awsClient) RunInstances(ctx context.Context, input *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
	return &ec2.RunInstancesOutput{
		Instances: []ec2types.Instance{
			{
				ImageId:    aws.String("ami-a9acbbd6"),
				InstanceId: aws.String("i-02fcb933c5da7085c"),
				State: &ec2types.InstanceState{
					Code: aws.Int32(16),
				},
			},
		},
	}, nil
}

func (c *awsClient) DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{
			{
				Instances: []ec2types.Instance{
					{
						ImageId:    aws.String("ami-a9acbbd6"),
						InstanceId: aws.String("i-02fcb933c5da7085c"),
						State: &ec2types.InstanceState{
							Name: ec2types.InstanceStateNameRunning,
							Code: aws.Int32(16),
						},
						LaunchTime: aws.Time(time.Now()),
					},
				},
			},
		},
	}, nil
}

func (c *awsClient) DescribeInstanceTypes(ctx context.Context, input *ec2.DescribeInstanceTypesInput) (*ec2.DescribeInstanceTypesOutput, error) {
	return &ec2.DescribeInstanceTypesOutput{
		InstanceTypes: []ec2types.InstanceTypeInfo{
			{
				InstanceType: ec2types.InstanceTypeM4Large,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(8192),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(2),
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						ec2types.ArchitectureTypeX8664,
					},
				},
			},
			{
				InstanceType: ec2types.InstanceTypeA12xlarge,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(16384),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(8),
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						ec2types.ArchitectureTypeX8664,
					},
				},
			},
			{
				InstanceType: ec2types.InstanceTypeP216xlarge,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(749568),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(64),
				},
				GpuInfo: &ec2types.GpuInfo{
					Gpus: []ec2types.GpuDeviceInfo{
						{
							Name:         aws.String("K80"),
							Manufacturer: aws.String("NVIDIA"),
							Count:        aws.Int32(16),
							MemoryInfo: &ec2types.GpuDeviceMemoryInfo{
								SizeInMiB: aws.Int32(12288),
							},
						},
					},
					TotalGpuMemoryInMiB: aws.Int32(196608),
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						ec2types.ArchitectureTypeX8664,
					},
				},
			},
			{
				InstanceType: ec2types.InstanceTypeG4adXlarge,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(16384),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(4),
				},
				GpuInfo: &ec2types.GpuInfo{
					Gpus: []ec2types.GpuDeviceInfo{
						{
							Name:         aws.String("Radeon Pro V520"),
							Manufacturer: aws.String("AMD"),
							Count:        aws.Int32(1),
							MemoryInfo: &ec2types.GpuDeviceMemoryInfo{
								SizeInMiB: aws.Int32(8192),
							},
						},
					},
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						ec2types.ArchitectureTypeX8664,
					},
				},
			},
			{
				InstanceType: ec2types.InstanceTypeM6g4xlarge,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(65536),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(16),
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						ec2types.ArchitectureTypeArm64,
					},
				},
			},
			{
				// This instance type misses the specification of the CPU Architecture.
				InstanceType: ec2types.InstanceTypeM6i8xlarge,
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(131072),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(32),
				},
			},
			{
				// This instance type reports a wrong specification of the CPU Architecture.
				InstanceType: "m6h.8xlarge",
				MemoryInfo: &ec2types.MemoryInfo{
					SizeInMiB: aws.Int64(131072),
				},
				VCpuInfo: &ec2types.VCpuInfo{
					DefaultVCpus: aws.Int32(32),
				},
				ProcessorInfo: &ec2types.ProcessorInfo{
					SupportedArchitectures: []ec2types.ArchitectureType{
						"wrong-arch",
					},
				},
			},
		},
	}, nil
}

func (c *awsClient) DescribeHosts(ctx context.Context, input *ec2.DescribeHostsInput) (*ec2.DescribeHostsOutput, error) {
	return &ec2.DescribeHostsOutput{}, nil
}

func (c *awsClient) AllocateHosts(ctx context.Context, input *ec2.AllocateHostsInput) (*ec2.AllocateHostsOutput, error) {
	return &ec2.AllocateHostsOutput{
		HostIds: []string{"h-0123456789abcdef0"},
	}, nil
}

func (c *awsClient) ReleaseHosts(ctx context.Context, input *ec2.ReleaseHostsInput) (*ec2.ReleaseHostsOutput, error) {
	return &ec2.ReleaseHostsOutput{
		Successful:   input.HostIds,
		Unsuccessful: []ec2types.UnsuccessfulItem{},
	}, nil
}

func (c *awsClient) TerminateInstances(ctx context.Context, input *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
	return &ec2.TerminateInstancesOutput{}, nil
}

func (c *awsClient) DescribeVolumes(ctx context.Context, input *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	return &ec2.DescribeVolumesOutput{}, nil
}

func (c *awsClient) CreateTags(ctx context.Context, input *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
	return &ec2.CreateTagsOutput{}, nil
}

func (c *awsClient) CreatePlacementGroup(ctx context.Context, input *ec2.CreatePlacementGroupInput) (*ec2.CreatePlacementGroupOutput, error) {
	return &ec2.CreatePlacementGroupOutput{}, nil
}

func (c *awsClient) DeletePlacementGroup(ctx context.Context, input *ec2.DeletePlacementGroupInput) (*ec2.DeletePlacementGroupOutput, error) {
	return &ec2.DeletePlacementGroupOutput{}, nil
}

func (c *awsClient) RegisterInstancesWithLoadBalancer(ctx context.Context, input *elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error) {
	return &elb.RegisterInstancesWithLoadBalancerOutput{}, nil
}

func (c *awsClient) ELBv2DescribeLoadBalancers(ctx context.Context, input *elbv2.DescribeLoadBalancersInput) (*elbv2.DescribeLoadBalancersOutput, error) {
	return &elbv2.DescribeLoadBalancersOutput{}, nil
}

func (c *awsClient) ELBv2DescribeTargetGroups(ctx context.Context, input *elbv2.DescribeTargetGroupsInput) (*elbv2.DescribeTargetGroupsOutput, error) {
	return &elbv2.DescribeTargetGroupsOutput{}, nil
}

func (c *awsClient) ELBv2DescribeTargetHealth(ctx context.Context, input *elbv2.DescribeTargetHealthInput) (*elbv2.DescribeTargetHealthOutput, error) {
	return &elbv2.DescribeTargetHealthOutput{}, nil
}

func (c *awsClient) ELBv2RegisterTargets(ctx context.Context, input *elbv2.RegisterTargetsInput) (*elbv2.RegisterTargetsOutput, error) {
	return &elbv2.RegisterTargetsOutput{}, nil
}

func (c *awsClient) ELBv2DeregisterTargets(ctx context.Context, input *elbv2.DeregisterTargetsInput) (*elbv2.DeregisterTargetsOutput, error) {
	return &elbv2.DeregisterTargetsOutput{}, nil
}

// NewClient creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use either the cluster AWS credentials
// secret if defined (i.e. in the root cluster),
// otherwise the IAM profile of the master where the actuator will run. (target clusters)
func NewClient(kubeClient kubernetes.Interface, secretName, namespace, region string) (client.Client, error) {
	return &awsClient{}, nil
}
