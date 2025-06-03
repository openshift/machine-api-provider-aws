/*
Copyright The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package machineset

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/service/ec2"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gtypes "github.com/onsi/gomega/types"
	openshiftfeatures "github.com/openshift/api/features"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/library-go/pkg/features"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	fakeawsclient "github.com/openshift/machine-api-provider-aws/pkg/client/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("MachineSetReconciler", func() {
	var c client.Client
	var stopMgr context.CancelFunc
	var fakeRecorder *record.FakeRecorder
	var namespace *corev1.Namespace
	fakeClient, err := fakeawsclient.NewClient(nil, "", "", "")
	Expect(err).ToNot(HaveOccurred())
	awsClientBuilder := func(client client.Client, secretName, namespace, region string, configManagedClient client.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
		return fakeClient, nil
	}

	BeforeEach(func() {
		mgr, err := manager.New(cfg, manager.Options{
			Metrics: server.Options{
				BindAddress: "0",
			}})
		Expect(err).ToNot(HaveOccurred())

		gate, err := newDefaultMutableFeatureGate()
		Expect(err).NotTo(HaveOccurred())

		r := Reconciler{
			Client:             mgr.GetClient(),
			Log:                log.Log,
			AwsClientBuilder:   awsClientBuilder,
			InstanceTypesCache: NewInstanceTypesCache(),
			Gate:               gate,
		}
		Expect(r.SetupWithManager(mgr, controller.Options{
			SkipNameValidation: ptr.To(true),
		})).To(Succeed())

		fakeRecorder = record.NewFakeRecorder(1)
		r.recorder = fakeRecorder

		c = mgr.GetClient()
		stopMgr = StartTestManager(mgr)

		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mhc-test-"}}
		Expect(c.Create(ctx, namespace)).To(Succeed())
	})

	AfterEach(func() {
		Expect(deleteMachineSets(c, namespace.Name)).To(Succeed())
		stopMgr()
	})

	type reconcileTestCase = struct {
		instanceType        string
		existingAnnotations map[string]string
		expectedAnnotations map[string]string
		expectedEvents      []string
	}

	DescribeTable("when reconciling MachineSets", func(rtc reconcileTestCase) {
		machineSet, err := newTestMachineSet(namespace.Name, rtc.instanceType, rtc.existingAnnotations)
		Expect(err).ToNot(HaveOccurred())

		Expect(c.Create(ctx, machineSet)).To(Succeed())

		Eventually(func() map[string]string {
			m := &machinev1beta1.MachineSet{}
			key := client.ObjectKey{Namespace: machineSet.Namespace, Name: machineSet.Name}
			err := c.Get(ctx, key, m)
			if err != nil {
				return nil
			}
			annotations := m.GetAnnotations()
			if annotations != nil {
				return annotations
			}
			// Return an empty map to distinguish between empty annotations and errors
			return make(map[string]string)
		}, timeout).Should(Equal(rtc.expectedAnnotations))

		// Check which event types were sent
		Eventually(fakeRecorder.Events, timeout).Should(HaveLen(len(rtc.expectedEvents)))
		receivedEvents := []string{}
		eventMatchers := []gtypes.GomegaMatcher{}
		for _, ev := range rtc.expectedEvents {
			receivedEvents = append(receivedEvents, <-fakeRecorder.Events)
			eventMatchers = append(eventMatchers, ContainSubstring(fmt.Sprintf(" %s ", ev)))
		}
		Expect(receivedEvents).To(ConsistOf(eventMatchers))
	},
		Entry("with no instanceType set", reconcileTestCase{
			instanceType:        "",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: make(map[string]string),
			expectedEvents:      []string{"FailedUpdate"},
		}),
		Entry("with a a1.2xlarge", reconcileTestCase{
			instanceType:        "a1.2xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "8",
				memoryKey: "16384",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectedEvents: []string{},
		}),
		Entry("with a p2.16xlarge", reconcileTestCase{
			instanceType:        "p2.16xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "64",
				memoryKey: "749568",
				gpuKey:    "16",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectedEvents: []string{},
		}),
		Entry("with existing annotations", reconcileTestCase{
			instanceType: "a1.2xlarge",
			existingAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			expectedAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
				cpuKey:     "8",
				memoryKey:  "16384",
				gpuKey:     "0",
				labelsKey:  "kubernetes.io/arch=amd64",
			},
			expectedEvents: []string{},
		}),
		Entry("with a m6g.4xlarge (aarch64)", reconcileTestCase{
			instanceType:        "m6g.4xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "16",
				memoryKey: "65536",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=arm64",
			},
			expectedEvents: []string{},
		}),
		Entry("with an instance type missing the supported architecture (default to amd64)", reconcileTestCase{
			instanceType:        "m6i.8xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "32",
				memoryKey: "131072",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectedEvents: []string{},
		}),
		Entry("with an unrecognized supported architecture (default to amd64)", reconcileTestCase{
			instanceType:        "m6h.8xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "32",
				memoryKey: "131072",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectedEvents: []string{},
		}),
		Entry("with an invalid instanceType", reconcileTestCase{
			instanceType: "invalid",
			existingAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			expectedAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			expectedEvents: []string{"FailedUpdate"},
		}),
	)
})

func deleteMachineSets(c client.Client, namespaceName string) error {
	machineSets := &machinev1beta1.MachineSetList{}
	err := c.List(ctx, machineSets, client.InNamespace(namespaceName))
	if err != nil {
		return err
	}

	for _, ms := range machineSets.Items {
		err := c.Delete(ctx, &ms)
		if err != nil {
			return err
		}
	}

	Eventually(func() error {
		machineSets := &machinev1beta1.MachineSetList{}
		err := c.List(ctx, machineSets)
		if err != nil {
			return err
		}
		if len(machineSets.Items) > 0 {
			return fmt.Errorf("machineSets not deleted")
		}
		return nil
	}, timeout).Should(Succeed())

	return nil
}

func TestReconcile(t *testing.T) {
	testCases := []struct {
		name                string
		instanceType        string
		existingAnnotations map[string]string
		expectedAnnotations map[string]string
		expectErr           bool
	}{
		{
			name:                "with no instanceType set",
			instanceType:        "",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: make(map[string]string),
			// Expect no error and only log entry in such case as we don't update instance types dynamically
			expectErr: false,
		},
		{
			name:                "with a a1.2xlarge",
			instanceType:        "a1.2xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "8",
				memoryKey: "16384",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectErr: false,
		},
		{
			name:                "with a p2.16xlarge",
			instanceType:        "p2.16xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "64",
				memoryKey: "749568",
				gpuKey:    "16",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectErr: false,
		},
		{
			name:         "with existing annotations",
			instanceType: "a1.2xlarge",
			existingAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			expectedAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
				cpuKey:     "8",
				memoryKey:  "16384",
				gpuKey:     "0",
				labelsKey:  "kubernetes.io/arch=amd64",
			},
			expectErr: false,
		},
		{
			name:         "with an invalid instanceType",
			instanceType: "invalid",
			existingAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			expectedAnnotations: map[string]string{
				"existing": "annotation",
				"annother": "existingAnnotation",
			},
			// Expect no error and only log entry in such case as we don't update instance types dynamically
			expectErr: false,
		},
		{
			name:                "with a m6g.4xlarge (aarch64)",
			instanceType:        "m6g.4xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "16",
				memoryKey: "65536",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=arm64",
			},
			expectErr: false,
		},
		{
			name:                "with an instance type missing the supported architecture (default to amd64)",
			instanceType:        "m6i.8xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "32",
				memoryKey: "131072",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectErr: false,
		},
		{
			name:                "with an unrecognized supported architecture (default to amd64)",
			instanceType:        "m6h.8xlarge",
			existingAnnotations: make(map[string]string),
			expectedAnnotations: map[string]string{
				cpuKey:    "32",
				memoryKey: "131072",
				gpuKey:    "0",
				labelsKey: "kubernetes.io/arch=amd64",
			},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(tt *testing.T) {
			g := NewWithT(tt)

			machineSet, err := newTestMachineSet("default", tc.instanceType, tc.existingAnnotations)
			g.Expect(err).ToNot(HaveOccurred())

			fakeClient, err := fakeawsclient.NewClient(nil, "", "", "")
			Expect(err).ToNot(HaveOccurred())
			awsClientBuilder := func(client client.Client, secretName, namespace, region string, configManagedClient client.Client, regionCache awsclient.RegionCache) (awsclient.Client, error) {
				return fakeClient, nil
			}

			r := Reconciler{
				recorder:           record.NewFakeRecorder(1),
				AwsClientBuilder:   awsClientBuilder,
				InstanceTypesCache: NewInstanceTypesCache(),
			}

			_, err = r.reconcile(machineSet)
			g.Expect(err != nil).To(Equal(tc.expectErr))
			g.Expect(machineSet.Annotations).To(Equal(tc.expectedAnnotations))
		})
	}
}

func TestNormalizeArchitecture(t *testing.T) {
	testCases := []struct {
		architecture string
		expected     normalizedArch
	}{
		{
			architecture: ec2.ArchitectureTypeX8664,
			expected:     ArchitectureAmd64,
		},
		{
			architecture: ec2.ArchitectureTypeArm64,
			expected:     ArchitectureArm64,
		},
		{
			architecture: "unknown",
			expected:     ArchitectureAmd64,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.architecture, func(tt *testing.T) {
			g := NewWithT(tt)
			g.Expect(normalizeArchitecture(tc.architecture)).To(Equal(tc.expected))
		})
	}
}

func newTestMachineSet(namespace string, instanceType string, existingAnnotations map[string]string) (*machinev1beta1.MachineSet, error) {
	// Copy anntotations map so we don't modify the input
	annotations := make(map[string]string)
	for k, v := range existingAnnotations {
		annotations[k] = v
	}

	machineProviderSpec := &machinev1beta1.AWSMachineProviderConfig{
		InstanceType: instanceType,
		CredentialsSecret: &corev1.LocalObjectReference{
			Name: "test-credentials",
		},
	}
	providerSpec, err := providerSpecFromMachine(machineProviderSpec)
	if err != nil {
		return nil, err
	}

	replicas := int32(1)
	return &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:  annotations,
			GenerateName: "test-machineset-",
			Namespace:    namespace,
		},
		Spec: machinev1beta1.MachineSetSpec{
			Replicas: &replicas,
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			},
		},
	}, nil
}

func providerSpecFromMachine(in *machinev1beta1.AWSMachineProviderConfig) (machinev1beta1.ProviderSpec, error) {
	bytes, err := json.Marshal(in)
	if err != nil {
		return machinev1beta1.ProviderSpec{}, err
	}
	return machinev1beta1.ProviderSpec{
		Value: &runtime.RawExtension{Raw: bytes},
	}, nil
}

func newDefaultMutableFeatureGate() (featuregate.MutableFeatureGate, error) {
	defaultMutableGate := feature.DefaultMutableFeatureGate
	if _, err := features.NewFeatureGateOptions(defaultMutableGate, openshiftfeatures.SelfManaged,
		openshiftfeatures.FeatureGateMachineAPIMigration); err != nil {
		return nil, fmt.Errorf("failed to set up default feature gate: %w", err)
	}
	if err := defaultMutableGate.SetFromMap(
		map[string]bool{
			"MachineAPIMigration": true,
		},
	); err != nil {
		return nil, fmt.Errorf("failed to set features from map: %w", err)
	}

	return defaultMutableGate, nil
}
