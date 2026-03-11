package client

import (
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildHTTPClient(t *testing.T) {
	cases := []struct {
		name             string
		cm               *corev1.ConfigMap
		expectHTTPClient bool
		expectErr        bool
	}{
		{
			name: "no configmap",
		},
		{
			name: "no CA bundle in configmap",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"other-key": "other-data",
				},
			},
		},
		{
			name: "invalid CA bundle",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": "not-a-valid-pem",
				},
			},
			expectErr: true,
		},
		{
			name: "custom CA bundle returns http client",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					// A self-signed test CA certificate for testing purposes
					"ca-bundle.pem": testCACert,
				},
			},
			expectHTTPClient: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			corev1.AddToScheme(scheme)
			resources := []runtime.Object{}
			if tc.cm != nil {
				resources = append(resources, tc.cm)
			}
			ctrlRuntimeClient := fake.NewClientBuilder().WithRuntimeObjects(resources...).Build()
			httpClient, err := buildHTTPClient(ctrlRuntimeClient)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error from buildHTTPClient: %v", err)
			}
			if tc.expectHTTPClient {
				if httpClient == nil {
					t.Fatalf("expected http client but got nil")
				}
				if httpClient.Transport == nil {
					t.Fatalf("expected http transport but got nil")
				}
				transport, ok := httpClient.Transport.(*http.Transport)
				if !ok {
					t.Fatalf("expected *http.Transport but got %T", httpClient.Transport)
				}
				if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
					t.Fatalf("expected custom root CAs to be set")
				}
			} else {
				if httpClient != nil {
					t.Fatalf("expected nil http client but got %v", httpClient)
				}
			}
		})
	}
}

// testCACert is a self-signed CA cert for testing
const testCACert = `-----BEGIN CERTIFICATE-----
MIIDCTCCAfGgAwIBAgIUX2pqiBRDuIAJa+qb9cSDvV41UkgwDQYJKoZIhvcNAQEL
BQAwFDESMBAGA1UEAwwJbG9jYWxob3N0MB4XDTI2MDMxMTE1MDYyNVoXDTM2MDMw
ODE1MDYyNVowFDESMBAGA1UEAwwJbG9jYWxob3N0MIIBIjANBgkqhkiG9w0BAQEF
AAOCAQ8AMIIBCgKCAQEA44U7qWQhKvrs4T25uGrc2TngenkZMZZAp/z73Yixxc2l
ED8hoPnP62VZRjzo52sJi0yUeE7iwd6eUMLiI9zW1hEmbxr7SsIi607SecSv9BcU
003WRLub5Wf2ieNYxvO7vouHXznLS6d6CQspBX/GaiTewsGxMwxnO1MmxDp9pXF9
uE4A3dFAokFiiVG/BMITKcTfKb4V6LdtyTVaGXuGndNC22nYT65ERzNQuzoa+PVQ
aTPgCj8s2Iq7N++iuyeSRRRSFKJcOl5uKx+AqCEw2hil/PVWEo3cy5kScBSeTkf1
07ZjiAutXYigmZjb7sRn7yYAg7km1879XL4SYZb1OwIDAQABo1MwUTAdBgNVHQ4E
FgQUXF2/HINx5L+iRIUWGQw+7iRzntAwHwYDVR0jBBgwFoAUXF2/HINx5L+iRIUW
GQw+7iRzntAwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEAH1xq
AXowfydyHkzGfpjxAW9KDoLfabUoIUlulX4E5rSDS7yvdsEUKz2CL4uswgBiVCVX
CCpxQ6mBvk9vKQEpBWE9xEsFCGQvh0g2mjRTh5evZGX3SoYTCNZ141XGqewdfH1Y
nEuJrdP658Mj8kvl0bfoLKcB5iX7y1b+sngjyjEd1AJoEsYkL70rSZ3MkXrvD9px
smuKIa5tVKyW2hfDggssXuj8XVJwqAJplsqfX9MPImp4DAYP/49DiEYtYi9ZtMj9
aynD7g5PKwh6Zu/T6c0QckOB/zdDz2u+kV5b2JSpAZxqzkOOwYZ9AEERbVqpUTO1
OA+GgX5epH8ZhaNYEw==
-----END CERTIFICATE-----`
