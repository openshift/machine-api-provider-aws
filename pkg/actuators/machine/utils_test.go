package machine

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/kubernetes/scheme"
)

func init() {
	// Add types to scheme
	machinev1beta1.AddToScheme(scheme.Scheme)
}

func TestExtractNodeAddresses(t *testing.T) {
	testCases := []struct {
		testcase          string
		instance          *ec2.Instance
		expectedAddresses []corev1.NodeAddress
		domainNames       []string
	}{
		{
			testcase: "one-public",
			instance: &ec2.Instance{
				PublicIpAddress: aws.String("1.1.1.1"),
				PublicDnsName:   aws.String("ec2.example.net"),
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeExternalIP, Address: "1.1.1.1"},
				{Type: corev1.NodeExternalDNS, Address: "ec2.example.net"},
			},
			domainNames: nil,
		},
		{
			testcase: "one-private",
			instance: &ec2.Instance{
				PrivateDnsName: aws.String("ec2.example.net"),
				NetworkInterfaces: []*ec2.InstanceNetworkInterface{
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(true),
								PrivateIpAddress: aws.String("10.0.0.5"),
							},
						},
					},
				},
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.example.net"},
				{Type: corev1.NodeHostName, Address: "ec2.example.net"},
			},
			domainNames: nil,
		},
		{
			testcase: "custom-domain",
			instance: &ec2.Instance{
				PrivateDnsName: aws.String("ec2.example.net"),
				NetworkInterfaces: []*ec2.InstanceNetworkInterface{
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(true),
								PrivateIpAddress: aws.String("10.0.0.5"),
							},
						},
					},
				},
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.example.net"},
				{Type: corev1.NodeHostName, Address: "ec2.example.net"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.openshift.com"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.openshift.io"},
			},
			domainNames: []string{"openshift.com", "openshift.io"},
		},
		{
			testcase: "custom-domain no duplicates",
			instance: &ec2.Instance{
				PrivateDnsName: aws.String("ec2.example.net"),
				NetworkInterfaces: []*ec2.InstanceNetworkInterface{
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(true),
								PrivateIpAddress: aws.String("10.0.0.5"),
							},
						},
					},
				},
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.example.net"},
				{Type: corev1.NodeHostName, Address: "ec2.example.net"},
			},
			domainNames: []string{"example.net", "example.net"},
		},
		{
			testcase: "multiple-private",
			instance: &ec2.Instance{
				PrivateDnsName: aws.String("ec2.example.net"),
				NetworkInterfaces: []*ec2.InstanceNetworkInterface{
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(true),
								PrivateIpAddress: aws.String("10.0.0.5"),
							},
						},
					},
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(false),
								PrivateIpAddress: aws.String("10.0.0.6"),
							},
						},
					},
				},
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeInternalIP, Address: "10.0.0.6"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.example.net"},
				{Type: corev1.NodeHostName, Address: "ec2.example.net"},
			},
			domainNames: nil,
		},
		{
			testcase: "ipv6-private",
			instance: &ec2.Instance{
				PrivateDnsName: aws.String("ec2.example.net"),
				NetworkInterfaces: []*ec2.InstanceNetworkInterface{
					{
						Status: aws.String(ec2.NetworkInterfaceStatusInUse),
						Ipv6Addresses: []*ec2.InstanceIpv6Address{
							{
								Ipv6Address: aws.String("2600:1f18:4254:5100:ef8a:7b65:7782:9248"),
							},
						},
						PrivateIpAddresses: []*ec2.InstancePrivateIpAddress{
							{
								Primary:          aws.Bool(true),
								PrivateIpAddress: aws.String("10.0.0.5"),
							},
						},
					},
				},
			},
			expectedAddresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "2600:1f18:4254:5100:ef8a:7b65:7782:9248"},
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeInternalDNS, Address: "ec2.example.net"},
				{Type: corev1.NodeHostName, Address: "ec2.example.net"},
			},
			domainNames: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testcase, func(t *testing.T) {
			addresses, err := extractNodeAddresses(tc.instance, tc.domainNames)
			if err != nil {
				t.Errorf("Unexpected extractNodeAddresses error: %v", err)
			}

			if !equality.Semantic.DeepEqual(addresses, tc.expectedAddresses) {
				t.Errorf("expected: %v, got: %v", tc.expectedAddresses, addresses)
			}
		})
	}
}

func TestFetchInfraResourceTags(t *testing.T) {
	testCases := []struct {
		testcase     string
		instance     *configv1.Infrastructure
		expectedTags map[string]interface{}
	}{
		{
			testcase: "UserTags_in_Spec",
			instance: &configv1.Infrastructure{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec: configv1.InfrastructureSpec{
					CloudConfig: configv1.ConfigMapFileReference{},
					PlatformSpec: configv1.PlatformSpec{
						Type: "AWS",
						AWS: &configv1.AWSPlatformSpec{
							ResourceTags: []configv1.AWSResourceTag{{Key: "team", Value: "CFE"}, {Key: "tester", Value: "member1"}, {Key: "region", Value: ""}},
						},
					},
				},
				Status: configv1.InfrastructureStatus{},
			},
			expectedTags: map[string]interface{}{"upd": map[string]string{"team": "CFE", "tester": "member1"}, "del": map[string]string{"region": ""}},
		},
		{
			testcase: "UserTags_in_Status",
			instance: &configv1.Infrastructure{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec:       configv1.InfrastructureSpec{},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: "",
						AWS: &configv1.AWSPlatformStatus{
							ResourceTags: []configv1.AWSResourceTag{{Key: "team", Value: "CFE"}, {Key: "tester", Value: "member1"}},
						},
					},
				},
			},
			expectedTags: map[string]interface{}{"upd": map[string]string{"team": "CFE", "tester": "member1"}},
		},
		{
			testcase: "UserTags_in_Spec_and_Status_MergeCase",
			instance: &configv1.Infrastructure{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec: configv1.InfrastructureSpec{
					CloudConfig: configv1.ConfigMapFileReference{},
					PlatformSpec: configv1.PlatformSpec{
						Type: "AWS",
						AWS: &configv1.AWSPlatformSpec{
							ResourceTags: []configv1.AWSResourceTag{{Key: "team", Value: "NewCFE"}, {Key: "tester", Value: "NewMember"}, {Key: "region", Value: ""}},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: "",
						AWS: &configv1.AWSPlatformStatus{
							ResourceTags: []configv1.AWSResourceTag{{Key: "team", Value: "OldCFE"}, {Key: "tester", Value: "member1"}},
						},
					},
				},
			},
			expectedTags: map[string]interface{}{"upd": map[string]string{"team": "NewCFE", "tester": "NewMember"}, "del": map[string]string{"region": ""}},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.testcase, func(t *testing.T) {
			tags := fetchInfraResourceTags(tc.instance)
			if !assertUserTags(tc.expectedTags, tags) {
				t.Errorf("expected: %v, got: %v", tc.expectedTags, tags)
			}
		})
	}
}

func assertUserTags(expected, actual map[string]interface{}) bool {
	if len(expected) != len(actual) {
		return false
	}

	if _, ok := expected["upd"]; ok {
		tags, ok := actual["upd"].(map[string]string)
		if !ok {
			return false
		}
		for key, value := range tags {
			if expectedValue, ok := expected[key]; !ok || expectedValue != value {
				return false
			}
		}
	}

	if _, ok := expected["del"]; ok {
		tags, ok := actual["del"].(map[string]string)
		if !ok {
			return false
		}
		for key, value := range tags {
			if expectedValue, ok := expected[key]; !ok || expectedValue != value {
				return false
			}
		}
	}

	return true
}
