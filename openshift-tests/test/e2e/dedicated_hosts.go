package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	machineutil "github.com/openshift/machine-api-operator/test/e2e"
	machine "github.com/openshift/machine-api-provider-aws/pkg/actuators/machine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	clientName = "machine-api-provider-aws-e2e"
)

var _ = Describe("[sig-cluster-lifecycle][OCPFeatureGate:AWSDedicatedHosts][platform:aws][Serial][Suite:openshift/conformance/serial] MAPA Dedicated Hosts", Label("AWS", "dedicated-hosts", "SLOW", "Conformance", "Serial"), func() {
	var (
		ctx        context.Context
		ec2Client  *ec2.Client
		err        error
		kubeClient *kubernetes.Clientset
		kubeConfig *rest.Config

		// Store the original machineset replicas to restore after tests
		originalReplicas map[string]int32
	)

	BeforeEach(func() {
		ctx = context.Background()
		originalReplicas = make(map[string]int32)

		// Get kube client
		kubeConfig, err = newClientConfigForTest()
		if err != nil {
			Fail(fmt.Sprintf("Failed to get kubeconfig: %v", err))
		}
		kubeClient = kubernetes.NewForConfigOrDie(rest.AddUserAgent(kubeConfig, clientName))
		Expect(kubeClient).NotTo(BeNil())

		// Check to see if we have any machineset with dedicated hosts
		machineSets, err := machineutil.GetMachineSets(kubeConfig)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(machineSets.Items)).NotTo(Equal(0), "cluster should have at least 1 worker machine set created by installer")

		if !existsDedicatedHost(machineSets) {
			Skip("No dedicated hosts found - skipping all dedicated host tests")
		}

		// Get region from first machineset
		region, err := getRegionFromMachineSet(&machineSets.Items[0])
		Expect(err).NotTo(HaveOccurred())

		// Initialize EC2 client
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		Expect(err).NotTo(HaveOccurred())
		ec2Client = ec2.NewFromConfig(awsCfg)
	})

	AfterEach(func() {
		// Restore original machineset replicas
		for name, replicas := range originalReplicas {
			By(fmt.Sprintf("Restoring machineset %s to %d replicas", name, replicas))
			err := machineutil.ScaleMachineSet(kubeConfig, name, int(replicas))
			if err != nil {
				GinkgoWriter.Printf("Warning: Failed to restore machineset %s: %v\n", name, err)
			}
		}
	})

	Context("Scaling MachineSet with Dedicated Hosts", func() {
		It("should scale up a machineset and place instances on dedicated hosts", ginkgo.Informing(), func() {
			machineSets, err := machineutil.GetMachineSets(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			// Find first machineset with dedicated host configuration
			var targetMS *machinev1beta1.MachineSet
			for i := range machineSets.Items {
				ms := &machineSets.Items[i]
				providerSpec, err := machine.ProviderSpecFromRawExtension(ms.Spec.Template.Spec.ProviderSpec.Value)
				Expect(err).NotTo(HaveOccurred())

				if providerSpec.Placement.Host != nil && providerSpec.Placement.Host.DedicatedHost != nil {
					targetMS = ms
					break
				}
			}
			Expect(targetMS).NotTo(BeNil(), "no machineset with dedicated host found")

			// Store original replica count
			originalReplicas[targetMS.Name] = *targetMS.Spec.Replicas
			currentReplicas := int(*targetMS.Spec.Replicas)
			targetReplicas := currentReplicas + 1

			By(fmt.Sprintf("Scaling machineset %s from %d to %d replicas", targetMS.Name, currentReplicas, targetReplicas))
			err = machineutil.ScaleMachineSet(kubeConfig, targetMS.Name, targetReplicas)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for new machines to be running")
			Eventually(func() (int, error) {
				return countRunningMachinesInMachineSet(ctx, kubeConfig, targetMS.Name)
			}, 10*time.Minute, 30*time.Second).Should(Equal(targetReplicas))

			By("Verifying instances are placed on dedicated hosts")
			machines := getMachinesInMachineSet(kubeConfig, targetMS.Name)
			Expect(len(machines)).To(BeNumerically(">=", targetReplicas))

			providerSpec, err := machine.ProviderSpecFromRawExtension(targetMS.Spec.Template.Spec.ProviderSpec.Value)
			Expect(err).NotTo(HaveOccurred())

			for _, m := range machines {
				if m.Status.ProviderStatus == nil {
					continue
				}

				providerStatus, err := machine.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				Expect(err).NotTo(HaveOccurred())

				if providerStatus.InstanceID == nil || *providerStatus.InstanceID == "" {
					continue
				}

				instanceID := *providerStatus.InstanceID
				By(fmt.Sprintf("Verifying instance %s is on a dedicated host", instanceID))

				hostID, err := getInstanceHostID(ctx, ec2Client, instanceID)
				Expect(err).NotTo(HaveOccurred())
				Expect(hostID).NotTo(BeEmpty(), "instance should be placed on a dedicated host")

				// If user-provided host ID, verify it matches
				if providerSpec.Placement.Host.DedicatedHost.AllocationStrategy != nil &&
					*providerSpec.Placement.Host.DedicatedHost.AllocationStrategy == "UserProvided" &&
					providerSpec.Placement.Host.DedicatedHost.ID != "" {
					expectedHostID := providerSpec.Placement.Host.DedicatedHost.ID
					Expect(hostID).To(Equal(expectedHostID), "instance should be on the specified dedicated host")
				}

				// Verify tenancy is "host"
				tenancy := getInstanceTenancy(ctx, ec2Client, instanceID)
				Expect(tenancy).To(Equal("host"), "instance tenancy should be 'host'")
			}
		})

		It("should scale down a machineset with dedicated hosts and cleanup properly", ginkgo.Informing(), func() {
			machineSets, err := machineutil.GetMachineSets(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			// Find machineset with dedicated host and at least 2 replicas
			var targetMS *machinev1beta1.MachineSet
			for i := range machineSets.Items {
				ms := &machineSets.Items[i]
				providerSpec, err := machine.ProviderSpecFromRawExtension(ms.Spec.Template.Spec.ProviderSpec.Value)
				Expect(err).NotTo(HaveOccurred())

				if providerSpec.Placement.Host != nil &&
					providerSpec.Placement.Host.DedicatedHost != nil &&
					ms.Spec.Replicas != nil && *ms.Spec.Replicas >= 1 {
					targetMS = ms
					break
				}
			}

			if targetMS == nil {
				Skip("No machineset with dedicated host and at least 1 replicas found")
			}

			originalReplicas[targetMS.Name] = *targetMS.Spec.Replicas
			currentReplicas := int(*targetMS.Spec.Replicas)
			targetReplicas := currentReplicas - 1

			// Get instance IDs before scaling down
			machinesBefore := getMachinesInMachineSet(kubeConfig, targetMS.Name)
			instanceIDsBefore := make([]string, 0)
			for _, m := range machinesBefore {
				if m.Status.ProviderStatus != nil {
					providerStatus, err := machine.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
					if err == nil && providerStatus.InstanceID != nil {
						instanceIDsBefore = append(instanceIDsBefore, *providerStatus.InstanceID)
					}
				}
			}

			By(fmt.Sprintf("Scaling down machineset %s from %d to %d replicas", targetMS.Name, currentReplicas, targetReplicas))
			err = machineutil.ScaleMachineSet(kubeConfig, targetMS.Name, targetReplicas)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for machines to be terminated")
			Eventually(func() (int, error) {
				return countRunningMachinesInMachineSet(ctx, kubeConfig, targetMS.Name)
			}, 10*time.Minute, 30*time.Second).Should(Equal(targetReplicas))

			By("Verifying instances are properly terminated")
			machinesAfter := getMachinesInMachineSet(kubeConfig, targetMS.Name)
			Expect(len(machinesAfter)).To(Equal(targetReplicas))

			// Verify at least one instance was terminated
			instanceIDsAfter := make([]string, 0)
			for _, m := range machinesAfter {
				if m.Status.ProviderStatus != nil {
					providerStatus, err := machine.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
					if err == nil && providerStatus.InstanceID != nil {
						instanceIDsAfter = append(instanceIDsAfter, *providerStatus.InstanceID)
					}
				}
			}

			Expect(len(instanceIDsBefore) - len(instanceIDsAfter)).To(BeNumerically(">=", 1))
		})

		It("should scale machineset to zero and back up with dedicated hosts", ginkgo.Informing(), func() {
			machineSets, err := machineutil.GetMachineSets(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			// Find machineset with dedicated host
			var targetMS *machinev1beta1.MachineSet
			for i := range machineSets.Items {
				ms := &machineSets.Items[i]
				providerSpec, err := machine.ProviderSpecFromRawExtension(ms.Spec.Template.Spec.ProviderSpec.Value)
				Expect(err).NotTo(HaveOccurred())

				if providerSpec.Placement.Host != nil &&
					providerSpec.Placement.Host.DedicatedHost != nil &&
					ms.Spec.Replicas != nil && *ms.Spec.Replicas > 0 {
					targetMS = ms
					break
				}
			}

			if targetMS == nil {
				Skip("No machineset with dedicated host found")
			}

			originalReplicas[targetMS.Name] = *targetMS.Spec.Replicas

			By(fmt.Sprintf("Scaling machineset %s to 0 replicas", targetMS.Name))
			err = machineutil.ScaleMachineSet(kubeConfig, targetMS.Name, 0)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for all machines to be terminated")
			Eventually(func() (int, error) {
				return countRunningMachinesInMachineSet(ctx, kubeConfig, targetMS.Name)
			}, 10*time.Minute, 30*time.Second).Should(Equal(0))

			By("Scaling machineset back to 1 replica")
			err = machineutil.ScaleMachineSet(kubeConfig, targetMS.Name, 1)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for machine to be running")
			Eventually(func() (int, error) {
				return countRunningMachinesInMachineSet(ctx, kubeConfig, targetMS.Name)
			}, 10*time.Minute, 30*time.Second).Should(Equal(1))

			By("Verifying new instance is placed on dedicated host")
			machines := getMachinesInMachineSet(kubeConfig, targetMS.Name)
			Expect(len(machines)).To(Equal(1))

			m := machines[0]
			if m.Status.ProviderStatus != nil {
				providerStatus, err := machine.ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				Expect(err).NotTo(HaveOccurred())

				if providerStatus.InstanceID != nil && *providerStatus.InstanceID != "" {
					instanceID := *providerStatus.InstanceID
					hostID, err := getInstanceHostID(ctx, ec2Client, instanceID)
					Expect(err).NotTo(HaveOccurred())
					Expect(hostID).NotTo(BeEmpty(), "instance should be placed on a dedicated host")
				}
			}
		})

		It("should verify dedicated host capacity is tracked correctly when scaling", ginkgo.Informing(), func() {
			machineSets, err := machineutil.GetMachineSets(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			// Find machineset with user-provided dedicated host
			var targetMS *machinev1beta1.MachineSet
			var hostID string
			for i := range machineSets.Items {
				ms := &machineSets.Items[i]
				providerSpec, err := machine.ProviderSpecFromRawExtension(ms.Spec.Template.Spec.ProviderSpec.Value)
				Expect(err).NotTo(HaveOccurred())

				if providerSpec.Placement.Host != nil &&
					providerSpec.Placement.Host.DedicatedHost != nil &&
					providerSpec.Placement.Host.DedicatedHost.AllocationStrategy != nil &&
					*providerSpec.Placement.Host.DedicatedHost.AllocationStrategy == "UserProvided" &&
					providerSpec.Placement.Host.DedicatedHost.ID != "" {
					targetMS = ms
					hostID = providerSpec.Placement.Host.DedicatedHost.ID
					break
				}
			}

			if targetMS == nil {
				Skip("No machineset with user-provided dedicated host found")
			}

			By(fmt.Sprintf("Checking available capacity on dedicated host %s", hostID))
			availableCapacity, err := getDedicatedHostAvailableCapacity(ctx, ec2Client, hostID)
			Expect(err).NotTo(HaveOccurred())

			By(fmt.Sprintf("Dedicated host %s has %d available instance capacity", hostID, availableCapacity))

			// Get instance type from machineset
			providerSpec, err := machine.ProviderSpecFromRawExtension(targetMS.Spec.Template.Spec.ProviderSpec.Value)
			Expect(err).NotTo(HaveOccurred())
			instanceType := providerSpec.InstanceType

			By(fmt.Sprintf("Machineset uses instance type: %s", instanceType))

			originalReplicas[targetMS.Name] = *targetMS.Spec.Replicas
			currentReplicas := int(*targetMS.Spec.Replicas)

			// Only scale if there's available capacity
			if availableCapacity > 0 {
				targetReplicas := currentReplicas + 1
				By(fmt.Sprintf("Scaling machineset %s from %d to %d replicas", targetMS.Name, currentReplicas, targetReplicas))
				err = machineutil.ScaleMachineSet(kubeConfig, targetMS.Name, targetReplicas)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for new machine to be running")
				Eventually(func() (int, error) {
					return countRunningMachinesInMachineSet(ctx, kubeConfig, targetMS.Name)
				}, 10*time.Minute, 30*time.Second).Should(Equal(targetReplicas))

				By("Verifying capacity decreased on dedicated host")
				newAvailableCapacity, err := getDedicatedHostAvailableCapacity(ctx, ec2Client, hostID)
				Expect(err).NotTo(HaveOccurred())
				Expect(newAvailableCapacity).To(BeNumerically("<", availableCapacity), "available capacity should decrease after scaling up")
			} else {
				By(fmt.Sprintf("Skipping scale-up test: dedicated host %s has no available capacity", hostID))
			}
		})

		It("should create a machine with dynamic dedicated host allocation", ginkgo.Informing(), func() {
			machineSets, err := machineutil.GetMachineSets(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			// Find a machineset to use as template (preferably one without dedicated hosts to avoid conflicts)
			var templateMS *machinev1beta1.MachineSet
			for i := range machineSets.Items {
				ms := &machineSets.Items[i]
				if ms.Spec.Replicas != nil && *ms.Spec.Replicas > 0 {
					templateMS = ms
					break
				}
			}
			Expect(templateMS).NotTo(BeNil(), "no machineset found to use as template")

			// Get the provider spec from the template
			templateProviderSpec, err := machine.ProviderSpecFromRawExtension(templateMS.Spec.Template.Spec.ProviderSpec.Value)
			Expect(err).NotTo(HaveOccurred())

			// Create a new provider spec with dynamic dedicated host allocation
			newProviderSpec := templateProviderSpec.DeepCopy()

			// Configure for dynamic dedicated host allocation
			allocationStrategy := machinev1beta1.AllocationStrategyDynamic
			affinity := machinev1beta1.HostAffinityDedicatedHost
			newProviderSpec.Placement.Tenancy = machinev1beta1.HostTenancy
			newProviderSpec.Placement.Host = &machinev1beta1.HostPlacement{
				Affinity: &affinity,
				DedicatedHost: &machinev1beta1.DedicatedHost{
					AllocationStrategy: &allocationStrategy,
					DynamicHostAllocation: &machinev1beta1.DynamicHostAllocationSpec{
						Tags: []machinev1beta1.TagSpecification{
							{
								Name:  "e2e-test",
								Value: "dynamic-dedicated-host",
							},
						},
					},
				},
			}

			// Marshal the new provider spec
			newProviderSpecRaw, err := json.Marshal(newProviderSpec)
			Expect(err).NotTo(HaveOccurred())

			// Get infrastructure to build machine name
			infra := machineutil.LoadInfra(kubeConfig)

			// Create unique machine name
			machineName := fmt.Sprintf("%s-dynamic-dh-test-%d", infra.Status.InfrastructureName, time.Now().Unix())

			// Build the machine object
			testMachine := &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      machineName,
					Namespace: machineutil.MachineAPINamespace,
					Labels: map[string]string{
						"machine.openshift.io/cluster-api-cluster": infra.Status.InfrastructureName,
						"e2e-test": "dynamic-dedicated-host",
					},
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{
							Raw: newProviderSpecRaw,
						},
					},
				},
			}

			By(fmt.Sprintf("Creating machine %s with dynamic dedicated host allocation", machineName))
			client, err := machineclient.NewForConfig(kubeConfig)
			Expect(err).NotTo(HaveOccurred())

			createdMachine, err := client.Machines(machineutil.MachineAPINamespace).Create(ctx, testMachine, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(createdMachine).NotTo(BeNil())

			// Ensure cleanup happens
			var dynamicHostID string
			defer func() {
				By(fmt.Sprintf("Cleaning up test machine %s", machineName))
				err := client.Machines(machineutil.MachineAPINamespace).Delete(ctx, machineName, metav1.DeleteOptions{})
				if err != nil {
					GinkgoWriter.Printf("Warning: Failed to delete test machine %s: %v\n", machineName, err)
				}

				// Wait for machine to be deleted
				Eventually(func() bool {
					_, err := client.Machines(machineutil.MachineAPINamespace).Get(ctx, machineName, metav1.GetOptions{})
					return err != nil
				}, 10*time.Minute, 30*time.Second).Should(BeTrue())

				// Verify the dynamically allocated host is released
				if dynamicHostID != "" {
					By(fmt.Sprintf("Verifying dynamically allocated host %s is released", dynamicHostID))
					Eventually(func() bool {
						state, err := getDedicatedHostState(ctx, ec2Client, dynamicHostID)
						if err != nil {
							// Host might already be deleted
							return true
						}
						// Host should be in released or pending-release state
						return state == "released" || state == "pending"
					}, 10*time.Minute, 30*time.Second).Should(BeTrue())
				}
			}()

			By("Waiting for machine to have a running instance")
			var instanceID string
			Eventually(func() bool {
				m, err := client.Machines(machineutil.MachineAPINamespace).Get(ctx, machineName, metav1.GetOptions{})
				if err != nil {
					return false
				}

				if m.Status.ProviderStatus == nil {
					return false
				}

				providerStatus, err := ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				if err != nil || providerStatus.InstanceID == nil || *providerStatus.InstanceID == "" {
					return false
				}

				instanceID = *providerStatus.InstanceID
				return true
			}, 10*time.Minute, 30*time.Second).Should(BeTrue())

			By(fmt.Sprintf("Machine created with instance ID: %s", instanceID))

			By("Verifying the machine status has dedicated host ID populated")
			var providerStatus *machinev1beta1.AWSMachineProviderStatus
			Eventually(func() bool {
				m, err := client.Machines(machineutil.MachineAPINamespace).Get(ctx, machineName, metav1.GetOptions{})
				if err != nil {
					return false
				}

				if m.Status.ProviderStatus == nil {
					return false
				}

				providerStatus, err = ProviderStatusFromRawExtension(m.Status.ProviderStatus)
				if err != nil {
					return false
				}

				// Check if dedicated host status is populated
				if providerStatus.DedicatedHost == nil || providerStatus.DedicatedHost.ID == nil || *providerStatus.DedicatedHost.ID == "" {
					return false
				}

				dynamicHostID = *providerStatus.DedicatedHost.ID
				return true
			}, 10*time.Minute, 30*time.Second).Should(BeTrue())

			By(fmt.Sprintf("Dynamic dedicated host allocated with ID: %s", dynamicHostID))
			Expect(dynamicHostID).To(MatchRegexp("^h-([0-9a-f]{8}|[0-9a-f]{17})$"), "host ID should match expected format")

			By("Verifying instance is running on the dynamically allocated host")
			hostID, err := getInstanceHostID(ctx, ec2Client, instanceID)
			Expect(err).NotTo(HaveOccurred())
			Expect(hostID).To(Equal(dynamicHostID), "instance should be on the dynamically allocated dedicated host")

			By("Verifying the dedicated host has the correct tags")
			tags, err := getDedicatedHostTags(ctx, ec2Client, dynamicHostID)
			Expect(err).NotTo(HaveOccurred())

			foundTag := false
			for _, tag := range tags {
				if tag.Key == "e2e-test" && tag.Value == "dynamic-dedicated-host" {
					foundTag = true
					break
				}
			}
			Expect(foundTag).To(BeTrue(), "dedicated host should have the e2e-test tag")

			By("Verifying instance tenancy is 'host'")
			tenancy := getInstanceTenancy(ctx, ec2Client, instanceID)
			Expect(tenancy).To(Equal("host"), "instance tenancy should be 'host'")

			By("Verifying the dedicated host is in 'available' state")
			state, err := getDedicatedHostState(ctx, ec2Client, dynamicHostID)
			Expect(err).NotTo(HaveOccurred())
			Expect(state).To(Equal("available"), "dedicated host should be available")
		})
	})
})

// Helper functions

// getRegionFromMachineSet extracts the AWS region from a machineset's availability zone
func getRegionFromMachineSet(machineSet *machinev1beta1.MachineSet) (string, error) {
	providerSpec, err := machine.ProviderSpecFromRawExtension(machineSet.Spec.Template.Spec.ProviderSpec.Value)
	if err != nil {
		return "", fmt.Errorf("failed to get provider spec: %w", err)
	}

	availabilityZone := providerSpec.Placement.AvailabilityZone
	if availabilityZone == "" {
		return "", fmt.Errorf("availability zone not specified in machineset")
	}

	// Extract region from availability zone (e.g., "us-east-1a" -> "us-east-1")
	// AWS availability zones are in the format <region><zone-letter>
	if len(availabilityZone) < 2 {
		return "", fmt.Errorf("invalid availability zone format: %s", availabilityZone)
	}
	region := availabilityZone[:len(availabilityZone)-1]
	return region, nil
}

// existsDedicatedHost checks if any machineset has a dedicated host configured
func existsDedicatedHost(machineSets *machinev1beta1.MachineSetList) bool {
	for _, machineSet := range machineSets.Items {
		providerSpec, err := machine.ProviderSpecFromRawExtension(machineSet.Spec.Template.Spec.ProviderSpec.Value)
		Expect(err).NotTo(HaveOccurred())

		if providerSpec.Placement.Host != nil && providerSpec.Placement.Host.DedicatedHost != nil {
			return true
		}
	}
	return false
}

// getInstanceHostID retrieves the host ID where an instance is running
func getInstanceHostID(ctx context.Context, ec2Client *ec2.Client, instanceID string) (string, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := ec2Client.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("no instance found")
	}

	instance := result.Reservations[0].Instances[0]
	if instance.Placement == nil || instance.Placement.HostId == nil {
		return "", fmt.Errorf("instance has no host placement information")
	}

	return *instance.Placement.HostId, nil
}

// getInstanceTenancy retrieves the tenancy setting of an instance
func getInstanceTenancy(ctx context.Context, ec2Client *ec2.Client, instanceID string) string {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := ec2Client.DescribeInstances(ctx, input)
	Expect(err).NotTo(HaveOccurred(), "Failed to describe instance")
	Expect(len(result.Reservations)).To(BeNumerically(">", 0), "No reservations found")
	Expect(len(result.Reservations[0].Instances)).To(BeNumerically(">", 0), "No instances found")

	instance := result.Reservations[0].Instances[0]
	if instance.Placement != nil && instance.Placement.Tenancy != "" {
		return string(instance.Placement.Tenancy)
	}

	return "default"
}

// getMachinesInMachineSet returns all machines belonging to a machineset
func getMachinesInMachineSet(kubeConfig *rest.Config, machineSetName string) []machinev1beta1.Machine {
	ctx := context.Background()

	// Get machine client
	client, err := machineclient.NewForConfig(kubeConfig)
	Expect(err).NotTo(HaveOccurred())

	// Get all machines in the namespace
	allMachines, err := client.Machines(machineutil.MachineAPINamespace).List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred())

	machines := make([]machinev1beta1.Machine, 0)
	for _, m := range allMachines.Items {
		// Check if this machine belongs to the target machineset
		for _, owner := range m.OwnerReferences {
			if owner.Name == machineSetName && owner.Kind == "MachineSet" {
				machines = append(machines, m)
				break
			}
		}
	}

	return machines
}

// countRunningMachinesInMachineSet counts the number of running machines in a machineset
func countRunningMachinesInMachineSet(ctx context.Context, kubeConfig *rest.Config, machineSetName string) (int, error) {
	machines := getMachinesInMachineSet(kubeConfig, machineSetName)

	count := 0
	for _, m := range machines {
		// Check if machine is running (has a node reference and is not being deleted)
		if m.Status.NodeRef != nil && m.DeletionTimestamp == nil {
			count++
		}
	}

	return count, nil
}

// getDedicatedHostAvailableCapacity returns the available instance capacity on a dedicated host
func getDedicatedHostAvailableCapacity(ctx context.Context, ec2Client *ec2.Client, hostID string) (int, error) {
	input := &ec2.DescribeHostsInput{
		HostIds: []string{hostID},
	}

	output, err := ec2Client.DescribeHosts(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to describe host: %w", err)
	}

	if len(output.Hosts) == 0 {
		return 0, fmt.Errorf("no host found with ID %s", hostID)
	}

	host := output.Hosts[0]

	// Calculate total available capacity across all instance types
	totalAvailable := 0
	if host.AvailableCapacity != nil && host.AvailableCapacity.AvailableInstanceCapacity != nil {
		for _, capacity := range host.AvailableCapacity.AvailableInstanceCapacity {
			if capacity.AvailableCapacity != nil {
				totalAvailable += int(*capacity.AvailableCapacity)
			}
		}
	}

	return totalAvailable, nil
}

// getDedicatedHostState retrieves the current state of a dedicated host
func getDedicatedHostState(ctx context.Context, ec2Client *ec2.Client, hostID string) (string, error) {
	input := &ec2.DescribeHostsInput{
		HostIds: []string{hostID},
	}

	output, err := ec2Client.DescribeHosts(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe host: %w", err)
	}

	if len(output.Hosts) == 0 {
		return "", fmt.Errorf("no host found with ID %s", hostID)
	}

	return string(output.Hosts[0].State), nil
}

// getDedicatedHostTags retrieves the tags of a dedicated host
func getDedicatedHostTags(ctx context.Context, ec2Client *ec2.Client, hostID string) ([]HostTag, error) {
	input := &ec2.DescribeHostsInput{
		HostIds: []string{hostID},
	}

	output, err := ec2Client.DescribeHosts(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe host: %w", err)
	}

	if len(output.Hosts) == 0 {
		return nil, fmt.Errorf("no host found with ID %s", hostID)
	}

	host := output.Hosts[0]
	tags := make([]HostTag, 0, len(host.Tags))
	for _, tag := range host.Tags {
		if tag.Key != nil && tag.Value != nil {
			tags = append(tags, HostTag{
				Key:   *tag.Key,
				Value: *tag.Value,
			})
		}
	}

	return tags, nil
}

// HostTag represents a tag on a dedicated host
type HostTag struct {
	Key   string
	Value string
}

// ProviderStatusFromRawExtension unmarshals a raw extension into an AWSMachineProviderStatus type
func ProviderStatusFromRawExtension(rawExtension *runtime.RawExtension) (*machinev1beta1.AWSMachineProviderStatus, error) {
	if rawExtension == nil {
		return nil, fmt.Errorf("provider status is nil")
	}

	providerStatus := &machinev1beta1.AWSMachineProviderStatus{}
	if err := json.Unmarshal(rawExtension.Raw, providerStatus); err != nil {
		return nil, fmt.Errorf("failed to unmarshal provider status: %w", err)
	}

	return providerStatus, nil
}
