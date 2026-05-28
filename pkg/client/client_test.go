package client

import (
	"io/ioutil"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUseCustomCABundle(t *testing.T) {
	cases := []struct {
		name             string
		cm               *corev1.ConfigMap
		expectedCABundle string
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
			name: "custom CA bundle",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": "a custom bundle",
				},
			},
			expectedCABundle: "a custom bundle",
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
			awsOptions := &session.Options{}
			err := useCustomCABundle(awsOptions, ctrlRuntimeClient)
			if err != nil {
				t.Fatalf("unexpected error from useCustomCABundle: %v", err)
			}
			actualCABundle := ""
			if awsOptions.CustomCABundle != nil {
				bundleBytes, err := ioutil.ReadAll(awsOptions.CustomCABundle)
				if err != nil {
					t.Fatalf("unexpected error reading bundle: %v", err)
				}
				actualCABundle = string(bundleBytes)
			}
			if a, e := actualCABundle, tc.expectedCABundle; a != e {
				t.Errorf("unexpected CA bundle: expected=%s; got %s", e, a)
			}
		})
	}
}

func testSession(accessKeyID string) *session.Session {
	return session.Must(session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(accessKeyID, "secret", "token"),
		Region:      aws.String("us-east-1"),
	}))
}

func TestRegionValidationCacheMiss(t *testing.T) {
	cache := NewRegionCache()
	sess := testSession("AKID-A")

	validated, err := cache.IsRegionValidated(sess, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss on first call, got hit")
	}
}

func TestRegionValidationCacheHit(t *testing.T) {
	cache := NewRegionCache()
	sess := testSession("AKID-A")

	if err := cache.SetRegionValidated(sess, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error from SetRegionValidated: %v", err)
	}

	validated, err := cache.IsRegionValidated(sess, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !validated {
		t.Error("expected cache hit after SetRegionValidated, got miss")
	}
}

func TestRegionValidationCacheExpiry(t *testing.T) {
	c := &regionCache{
		data:  map[string]DescribeRegionsData{},
		mutex: sync.RWMutex{},
	}
	sess := testSession("AKID-A")

	if err := c.SetRegionValidated(sess, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expire the cache entry
	data := c.data["AKID-A"]
	data.lastUpdated = time.Now().Add(-awsRegionsCacheExpirationDuration - time.Minute)
	c.data["AKID-A"] = data

	validated, err := c.IsRegionValidated(sess, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss after expiry, got hit")
	}
}

func TestRegionValidationCachePerCredentialIsolation(t *testing.T) {
	cache := NewRegionCache()
	sessA := testSession("AKID-A")
	sessB := testSession("AKID-B")

	if err := cache.SetRegionValidated(sessA, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	validated, err := cache.IsRegionValidated(sessB, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss for different credentials, got hit")
	}
}
