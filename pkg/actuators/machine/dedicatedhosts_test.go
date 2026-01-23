package machine

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/machine-api-provider-aws/pkg/client/mock"
	"k8s.io/utils/ptr"
)

func TestShouldAllocateDedicatedHost(t *testing.T) {
	tests := []struct {
		name      string
		placement *machinev1beta1.Placement
		expected  bool
	}{
		{
			name:      "nil placement",
			placement: nil,
			expected:  false,
		},
		{
			name:      "nil host",
			placement: &machinev1beta1.Placement{},
			expected:  false,
		},
		{
			name: "nil dedicated host",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{},
			},
			expected: false,
		},
		{
			name: "nil allocation strategy (defaults to UserProvided)",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{},
				},
			},
			expected: false,
		},
		{
			name: "user provided allocation strategy",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						AllocationStrategy: ptr.To(AllocationStrategyUserProvided),
					},
				},
			},
			expected: false,
		},
		{
			name: "dynamic allocation strategy",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						AllocationStrategy: ptr.To(AllocationStrategyDynamic),
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldAllocateDedicatedHost(tc.placement)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestGetDedicatedHostID(t *testing.T) {
	tests := []struct {
		name      string
		placement *machinev1beta1.Placement
		expected  string
	}{
		{
			name:      "nil placement",
			placement: nil,
			expected:  "",
		},
		{
			name: "user provided with ID",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						ID:                 "h-1234567890abcdef0",
						AllocationStrategy: ptr.To(AllocationStrategyUserProvided),
					},
				},
			},
			expected: "h-1234567890abcdef0",
		},
		{
			name: "dynamic allocation should return empty",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						ID:                 "h-1234567890abcdef0",
						AllocationStrategy: ptr.To(AllocationStrategyDynamic),
					},
				},
			},
			expected: "",
		},
		{
			name: "nil strategy defaults to UserProvided",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						ID: "h-1234567890abcdef0",
					},
				},
			},
			expected: "h-1234567890abcdef0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getDedicatedHostID(tc.placement)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestGetDynamicHostTags(t *testing.T) {
	tests := []struct {
		name      string
		placement *machinev1beta1.Placement
		expected  map[string]string
	}{
		{
			name:      "nil placement",
			placement: nil,
			expected:  nil,
		},
		{
			name: "nil dynamic host allocation",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{},
				},
			},
			expected: nil,
		},
		{
			name: "with tags",
			placement: &machinev1beta1.Placement{
				Host: &machinev1beta1.HostPlacement{
					DedicatedHost: &machinev1beta1.DedicatedHost{
						DynamicHostAllocation: &machinev1beta1.DynamicHostAllocationSpec{
							Tags: map[string]string{
								"key1": "value1",
								"key2": "value2",
							},
						},
					},
				},
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getDynamicHostTags(tc.placement)
			if len(result) != len(tc.expected) {
				t.Errorf("expected %d tags, got %d", len(tc.expected), len(result))
				return
			}
			for k, v := range tc.expected {
				if result[k] != v {
					t.Errorf("expected tag %s=%s, got %s=%s", k, v, k, result[k])
				}
			}
		})
	}
}

func TestAllocateDedicatedHost(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockAWSClient := mock.NewMockClient(mockCtrl)

	instanceType := "m5.large"
	availabilityZone := "us-east-1a"
	tags := map[string]string{
		"test-key": "test-value",
	}
	machineName := "test-machine"

	expectedHostID := "h-1234567890abcdef0"

	mockAWSClient.EXPECT().AllocateHosts(gomock.Any()).DoAndReturn(func(input *ec2.AllocateHostsInput) (*ec2.AllocateHostsOutput, error) {
		if *input.InstanceType != instanceType {
			t.Errorf("expected instance type %s, got %s", instanceType, *input.InstanceType)
		}
		if *input.AvailabilityZone != availabilityZone {
			t.Errorf("expected availability zone %s, got %s", availabilityZone, *input.AvailabilityZone)
		}
		if *input.Quantity != 1 {
			t.Errorf("expected quantity 1, got %d", *input.Quantity)
		}
		if *input.AutoPlacement != "off" {
			t.Errorf("expected auto placement off, got %s", *input.AutoPlacement)
		}

		return &ec2.AllocateHostsOutput{
			HostIds: []*string{aws.String(expectedHostID)},
		}, nil
	})

	hostID, err := allocateDedicatedHost(mockAWSClient, instanceType, availabilityZone, tags, machineName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hostID != expectedHostID {
		t.Errorf("expected host ID %s, got %s", expectedHostID, hostID)
	}
}

func TestReleaseDedicatedHost(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockAWSClient := mock.NewMockClient(mockCtrl)

	hostID := "h-1234567890abcdef0"
	machineName := "test-machine"

	mockAWSClient.EXPECT().ReleaseHosts(gomock.Any()).DoAndReturn(func(input *ec2.ReleaseHostsInput) (*ec2.ReleaseHostsOutput, error) {
		if len(input.HostIds) != 1 || *input.HostIds[0] != hostID {
			t.Errorf("expected host ID %s, got %v", hostID, input.HostIds)
		}

		return &ec2.ReleaseHostsOutput{
			Successful:   []*string{aws.String(hostID)},
			Unsuccessful: []*ec2.UnsuccessfulItem{},
		}, nil
	})

	err := releaseDedicatedHost(mockAWSClient, hostID, machineName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetAllocatedHostIDFromStatus(t *testing.T) {
	tests := []struct {
		name           string
		providerStatus *machinev1beta1.AWSMachineProviderStatus
		expected       string
	}{
		{
			name:           "nil provider status",
			providerStatus: nil,
			expected:       "",
		},
		{
			name:           "nil dedicated host status",
			providerStatus: &machinev1beta1.AWSMachineProviderStatus{},
			expected:       "",
		},
		{
			name: "nil host ID",
			providerStatus: &machinev1beta1.AWSMachineProviderStatus{
				DedicatedHost: &machinev1beta1.DedicatedHostStatus{},
			},
			expected: "",
		},
		{
			name: "with host ID",
			providerStatus: &machinev1beta1.AWSMachineProviderStatus{
				DedicatedHost: &machinev1beta1.DedicatedHostStatus{
					ID: ptr.To("h-1234567890abcdef0"),
				},
			},
			expected: "h-1234567890abcdef0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getAllocatedHostIDFromStatus(tc.providerStatus)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestSetAllocatedHostIDInStatus(t *testing.T) {
	hostID := "h-1234567890abcdef0"

	providerStatus := &machinev1beta1.AWSMachineProviderStatus{}
	setAllocatedHostIDInStatus(providerStatus, hostID)

	if providerStatus.DedicatedHost == nil {
		t.Error("DedicatedHost should not be nil")
		return
	}
	if providerStatus.DedicatedHost.ID == nil {
		t.Error("DedicatedHost.ID should not be nil")
		return
	}
	if *providerStatus.DedicatedHost.ID != hostID {
		t.Errorf("expected host ID %q, got %q", hostID, *providerStatus.DedicatedHost.ID)
	}
}

func TestClearAllocatedHostIDInStatus(t *testing.T) {
	providerStatus := &machinev1beta1.AWSMachineProviderStatus{
		DedicatedHost: &machinev1beta1.DedicatedHostStatus{
			ID: ptr.To("h-1234567890abcdef0"),
		},
	}

	clearAllocatedHostIDInStatus(providerStatus)

	if providerStatus.DedicatedHost.ID != nil {
		t.Errorf("expected host ID to be nil, got %q", *providerStatus.DedicatedHost.ID)
	}
}
