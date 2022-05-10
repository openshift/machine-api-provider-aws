package machine

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	mockaws "github.com/openshift/machine-api-provider-aws/pkg/client/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const testNamespace = "aws-test"

func machineWithSpec(spec *machinev1beta1.AWSMachineProviderConfig) *machinev1beta1.Machine {
	rawSpec, err := RawExtensionFromProviderSpec(spec)
	if err != nil {
		panic("Failed to encode raw extension from provider spec")
	}

	return &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "aws-test",
			Namespace: testNamespace,
		},
		Spec: machinev1beta1.MachineSpec{
			ProviderSpec: machinev1beta1.ProviderSpec{
				Value: rawSpec,
			},
		},
	}
}

func TestGetUserData(t *testing.T) {
	userDataSecretName := "aws-ignition"

	defaultProviderSpec := &machinev1beta1.AWSMachineProviderConfig{
		UserDataSecret: &corev1.LocalObjectReference{
			Name: userDataSecretName,
		},
	}

	testCases := []struct {
		testCase         string
		userDataSecret   *corev1.Secret
		providerSpec     *machinev1beta1.AWSMachineProviderConfig
		expectedUserdata []byte
		expectError      bool
	}{
		{
			testCase: "all good",
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      userDataSecretName,
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					userDataSecretKey: []byte("{}"),
				},
			},
			providerSpec:     defaultProviderSpec,
			expectedUserdata: []byte("{}"),
			expectError:      false,
		},
		{
			testCase:       "missing secret",
			userDataSecret: nil,
			providerSpec:   defaultProviderSpec,
			expectError:    true,
		},
		{
			testCase: "missing key in secret",
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      userDataSecretName,
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					"badKey": []byte("{}"),
				},
			},
			providerSpec: defaultProviderSpec,
			expectError:  true,
		},
		{
			testCase:         "no provider spec",
			userDataSecret:   nil,
			providerSpec:     nil,
			expectError:      false,
			expectedUserdata: nil,
		},
		{
			testCase:         "no user-data in provider spec",
			userDataSecret:   nil,
			providerSpec:     &machinev1beta1.AWSMachineProviderConfig{},
			expectError:      false,
			expectedUserdata: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			clientObjs := []runtime.Object{}

			if tc.userDataSecret != nil {
				clientObjs = append(clientObjs, tc.userDataSecret)
			}

			client := fake.NewFakeClient(clientObjs...)

			// Can't use newMachineScope because it tries to create an API
			// session, and other things unrelated to these tests.
			ms := &machineScope{
				Context:      context.Background(),
				client:       client,
				machine:      machineWithSpec(tc.providerSpec),
				providerSpec: tc.providerSpec,
			}

			userData, err := ms.getUserData()
			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if !bytes.Equal(userData, tc.expectedUserdata) {
				t.Errorf("Got: %q, Want: %q", userData, tc.expectedUserdata)
			}
		})
	}
}

func TestPatchMachine(t *testing.T) {
	g := NewWithT(t)

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "vendor", "github.com", "openshift", "api", "machine", "v1beta1")},
	}

	cfg, err := testEnv.Start()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
	defer func() {
		g.Expect(testEnv.Stop()).To(Succeed())
	}()

	// END: setup test environment

	k8sClient, err := client.New(cfg, client.Options{})
	g.Expect(err).ToNot(HaveOccurred())

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

	failedPhase := "Failed"

	providerStatus := &machinev1beta1.AWSMachineProviderStatus{}

	testCases := []struct {
		name   string
		mutate func(*machinev1beta1.Machine)
		expect func(*machinev1beta1.Machine) error
	}{
		{
			name: "Test changing labels",
			mutate: func(m *machinev1beta1.Machine) {
				m.ObjectMeta.Labels["testlabel"] = "test"
			},
			expect: func(m *machinev1beta1.Machine) error {
				if m.ObjectMeta.Labels["testlabel"] != "test" {
					return fmt.Errorf("label \"testlabel\" %q not equal expected \"test\"", m.ObjectMeta.Labels["test"])
				}
				return nil
			},
		},
		{
			name: "Test setting phase",
			mutate: func(m *machinev1beta1.Machine) {
				m.Status.Phase = &failedPhase
			},
			expect: func(m *machinev1beta1.Machine) error {
				if m.Status.Phase != nil && *m.Status.Phase == failedPhase {
					return nil
				}
				return fmt.Errorf("phase is nil or not equal expected \"Failed\"")
			},
		},
		{
			name: "Test setting provider status",
			mutate: func(m *machinev1beta1.Machine) {
				instanceID := "123"
				instanceState := "running"
				providerStatus.InstanceID = &instanceID
				providerStatus.InstanceState = &instanceState
			},
			expect: func(m *machinev1beta1.Machine) error {
				providerStatus, err := ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				if err != nil {
					return fmt.Errorf("unable to get provider status: %v", err)
				}

				if providerStatus.InstanceID == nil || *providerStatus.InstanceID != "123" {
					return fmt.Errorf("instanceID is nil or not equal expected \"123\"")
				}

				if providerStatus.InstanceState == nil || *providerStatus.InstanceState != "running" {
					return fmt.Errorf("instanceState is nil or not equal expected \"running\"")
				}

				return nil
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gs := NewWithT(t)

			machine, err := stubMachine()
			gs.Expect(err).ToNot(HaveOccurred())
			gs.Expect(machine).ToNot(BeNil())

			ctx := context.TODO()

			// Create the machine
			gs.Expect(k8sClient.Create(ctx, machine)).To(Succeed())
			defer func() {
				gs.Expect(k8sClient.Delete(ctx, machine)).To(Succeed())
			}()

			// Ensure the machine has synced to the cache
			getMachine := func() error {
				machineKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Name}
				return k8sClient.Get(ctx, machineKey, machine)
			}
			gs.Eventually(getMachine, timeout).Should(Succeed())

			machineScope, err := newMachineScope(machineScopeParams{
				client:  k8sClient,
				machine: machine,
				awsClientBuilder: func(client runtimeclient.Client, secretName, namespace, region string, configManagedClient runtimeclient.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
					return nil, nil
				},
			})

			if err != nil {
				t.Fatal(err)
			}

			tc.mutate(machineScope.machine)

			machineScope.providerStatus = providerStatus

			// Patch the machine and check the expectation from the test case
			gs.Expect(machineScope.patchMachine()).To(Succeed())
			checkExpectation := func() error {
				if err := getMachine(); err != nil {
					return err
				}
				return tc.expect(machine)
			}
			gs.Eventually(checkExpectation, timeout).Should(Succeed())

			// Check that resource version doesn't change if we call patchMachine() again
			machineResourceVersion := machine.ResourceVersion

			gs.Expect(machineScope.patchMachine()).To(Succeed())
			gs.Eventually(getMachine, timeout).Should(Succeed())
			gs.Expect(machine.ResourceVersion).To(Equal(machineResourceVersion))
		})
	}
}

func TestGetCustomDomainFromDHCP(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockAWSClient := mockaws.NewMockClient(mockCtrl)
	dhcpID := "someID"
	mockAWSClient.EXPECT().DescribeVpcs(gomock.Any()).Return(&ec2.DescribeVpcsOutput{
		Vpcs: []*ec2.Vpc{
			{DhcpOptionsId: &dhcpID},
		},
	}, nil).AnyTimes()

	testCases := []struct {
		description               string
		describeDhcpOptionsOutput ec2.DescribeDhcpOptionsOutput
		expected                  []string
	}{
		{
			description: "Returns two",
			expected:    []string{"openshift.com", "openshift.io"},
			describeDhcpOptionsOutput: ec2.DescribeDhcpOptionsOutput{
				DhcpOptions: []*ec2.DhcpOptions{
					{
						DhcpConfigurations: []*ec2.DhcpConfiguration{
							{
								Key: &dhcpDomainKeyName,
								Values: []*ec2.AttributeValue{
									{
										Value: aws.String("openshift.com openshift.io"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			description: "Returns one",
			expected:    []string{"openshift.com"},
			describeDhcpOptionsOutput: ec2.DescribeDhcpOptionsOutput{
				DhcpOptions: []*ec2.DhcpOptions{
					{
						DhcpConfigurations: []*ec2.DhcpConfiguration{
							{
								Key: &dhcpDomainKeyName,
								Values: []*ec2.AttributeValue{
									{
										Value: aws.String("openshift.com"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			description: "Returns a valid empty domain when domain-name is missing but domain-name-servers exists",
			expected:    []string{""},
			describeDhcpOptionsOutput: ec2.DescribeDhcpOptionsOutput{
				DhcpOptions: []*ec2.DhcpOptions{
					{
						DhcpConfigurations: []*ec2.DhcpConfiguration{
							{
								Key: aws.String("domain-name-servers"),
								Values: []*ec2.AttributeValue{
									{
										Value: aws.String("AmazonProvidedDNS"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		mS := machineScope{
			awsClient: mockAWSClient,
		}

		mockAWSClient.EXPECT().DescribeDHCPOptions(gomock.Any()).Return(&tc.describeDhcpOptionsOutput, nil).AnyTimes()

		got, err := mS.getCustomDomainFromDHCP(nil)
		if err != nil {
			t.Errorf("error when calling getCustomDomainFromDHCP: %v", err)
		}
		if !reflect.DeepEqual(got, tc.expected) {
			t.Errorf("Case: %s. Got: %#v, expected: %#v", tc.description, got, tc.expected)
		}
	}
}
