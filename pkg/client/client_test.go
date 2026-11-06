package client

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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
			optFns := []func(*awsconfig.LoadOptions) error{}
			err := addCustomCABundle(&optFns, ctrlRuntimeClient)
			if err != nil {
				t.Fatalf("unexpected error from addCustomCABundle: %v", err)
			}
			actualCABundle := ""
			if len(optFns) > 0 {
				// Apply options and check the CA bundle by loading a config
				opts := awsconfig.LoadOptions{}
				for _, fn := range optFns {
					if err := fn(&opts); err != nil {
						t.Fatalf("unexpected error applying option: %v", err)
					}
				}
				if opts.CustomCABundle != nil {
					bundleBytes, err := io.ReadAll(opts.CustomCABundle)
					if err != nil {
						t.Fatalf("unexpected error reading bundle: %v", err)
					}
					actualCABundle = string(bundleBytes)
				}
			}
			if a, e := actualCABundle, tc.expectedCABundle; a != e {
				t.Errorf("unexpected CA bundle: expected=%s; got %s", e, a)
			}
		})
	}
}

func testConfig(accessKeyID string) aws.Config {
	cfg, _ := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, "secret", "token")),
	)
	return cfg
}

func TestRegionValidationCacheMiss(t *testing.T) {
	cache := NewRegionCache()
	cfg := testConfig("AKID-A")
	ctx := context.Background()

	validated, err := cache.IsRegionValidated(ctx, cfg, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss on first call, got hit")
	}
}

func TestRegionValidationCacheHit(t *testing.T) {
	cache := NewRegionCache()
	cfg := testConfig("AKID-A")
	ctx := context.Background()

	if err := cache.SetRegionValidated(ctx, cfg, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error from SetRegionValidated: %v", err)
	}

	validated, err := cache.IsRegionValidated(ctx, cfg, "mx-central-1")
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
	cfg := testConfig("AKID-A")
	ctx := context.Background()

	if err := c.SetRegionValidated(ctx, cfg, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expire the cache entry
	data := c.data["AKID-A"]
	data.lastUpdated = time.Now().Add(-awsRegionsCacheExpirationDuration - time.Minute)
	c.data["AKID-A"] = data

	validated, err := c.IsRegionValidated(ctx, cfg, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss after expiry, got hit")
	}
}

func TestRegionValidationCachePerCredentialIsolation(t *testing.T) {
	cache := NewRegionCache()
	cfgA := testConfig("AKID-A")
	cfgB := testConfig("AKID-B")
	ctx := context.Background()

	if err := cache.SetRegionValidated(ctx, cfgA, "mx-central-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	validated, err := cache.IsRegionValidated(ctx, cfgB, "mx-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated {
		t.Error("expected cache miss for different credentials, got hit")
	}
}

type spyTransport struct {
	closed bool
}

func (s *spyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func (s *spyTransport) CloseIdleConnections() {
	s.closed = true
}

type spyHTTPClient struct {
	transport *spyTransport
}

func (s *spyHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return s.transport.RoundTrip(req)
}

func (s *spyHTTPClient) CloseIdleConnections() {
	s.transport.CloseIdleConnections()
}

func TestCloseIdleConnectionsClosesTransport(t *testing.T) {
	spy := &spyTransport{}
	c := &awsClient{
		cfg: aws.Config{
			Region:     "us-east-1",
			HTTPClient: &spyHTTPClient{transport: spy},
		},
	}
	c.CloseIdleConnections()
	if !spy.closed {
		t.Error("expected CloseIdleConnections to be called")
	}
}

func TestCloseIdleConnectionsNilConfig(t *testing.T) {
	c := &awsClient{}
	c.CloseIdleConnections()
}
