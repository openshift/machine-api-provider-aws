/*
Copyright 2018 The Kubernetes Authors.

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

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/openshift/machine-api-provider-aws/pkg/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	configv1 "github.com/openshift/api/config/v1"
	machineapiapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/rand"
)

//go:generate go run ../../vendor/github.com/golang/mock/mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

const (
	// AwsCredsSecretIDKey is secret key containing AWS KeyId
	AwsCredsSecretIDKey = "aws_access_key_id"
	// AwsCredsSecretAccessKey is secret key containing AWS Secret Key
	AwsCredsSecretAccessKey = "aws_secret_access_key"

	// GlobalInfrastuctureName default name for infrastructure object
	GlobalInfrastuctureName = "cluster"

	// KubeCloudConfigNamespace is the namespace where the kube cloud config ConfigMap is located
	KubeCloudConfigNamespace = "openshift-config-managed"
	// kubeCloudConfigName is the name of the kube cloud config ConfigMap
	kubeCloudConfigName = "kube-cloud-config"
	// cloudCABundleKey is the key in the kube cloud config ConfigMap where the custom CA bundle is located
	cloudCABundleKey = "ca-bundle.pem"
	// awsRegionsCacheExpirationDuration is the duration for which the AWS regions cache is valid
	awsRegionsCacheExpirationDuration = time.Minute * 30
)

var (
	sharedCredentialsFileMutex sync.Mutex
	sharedCredentialsFileName  = path.Join(os.TempDir(), "aws-shared-credentials"+rand.String(16))
)

// AwsClientBuilderFuncType is function type for building aws client
type AwsClientBuilderFuncType func(client client.Client, secretName, namespace, region string, configManagedClient client.Client, regionCache RegionCache) (Client, error)

// Client is a wrapper object for actual AWS SDK clients to allow for easier testing.
type Client interface {
	DescribeImages(ctx context.Context, input *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error)
	DescribeDHCPOptions(ctx context.Context, input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error)
	DescribeVpcs(ctx context.Context, input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, input *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	DescribeAvailabilityZones(ctx context.Context, input *ec2.DescribeAvailabilityZonesInput) (*ec2.DescribeAvailabilityZonesOutput, error)
	DescribeSecurityGroups(ctx context.Context, input *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribePlacementGroups(ctx context.Context, input *ec2.DescribePlacementGroupsInput) (*ec2.DescribePlacementGroupsOutput, error)
	DescribeInstanceTypes(ctx context.Context, input *ec2.DescribeInstanceTypesInput) (*ec2.DescribeInstanceTypesOutput, error)
	DescribeHosts(ctx context.Context, input *ec2.DescribeHostsInput) (*ec2.DescribeHostsOutput, error)
	AllocateHosts(ctx context.Context, input *ec2.AllocateHostsInput) (*ec2.AllocateHostsOutput, error)
	ReleaseHosts(ctx context.Context, input *ec2.ReleaseHostsInput) (*ec2.ReleaseHostsOutput, error)
	RunInstances(ctx context.Context, input *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error)
	DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(ctx context.Context, input *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error)
	DescribeVolumes(ctx context.Context, input *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	CreateTags(ctx context.Context, input *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error)
	CreatePlacementGroup(ctx context.Context, input *ec2.CreatePlacementGroupInput) (*ec2.CreatePlacementGroupOutput, error)
	DeletePlacementGroup(ctx context.Context, input *ec2.DeletePlacementGroupInput) (*ec2.DeletePlacementGroupOutput, error)

	RegisterInstancesWithLoadBalancer(ctx context.Context, input *elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error)
	ELBv2DescribeLoadBalancers(ctx context.Context, input *elbv2.DescribeLoadBalancersInput) (*elbv2.DescribeLoadBalancersOutput, error)
	ELBv2DescribeTargetGroups(ctx context.Context, input *elbv2.DescribeTargetGroupsInput) (*elbv2.DescribeTargetGroupsOutput, error)
	ELBv2DescribeTargetHealth(ctx context.Context, input *elbv2.DescribeTargetHealthInput) (*elbv2.DescribeTargetHealthOutput, error)
	ELBv2RegisterTargets(ctx context.Context, input *elbv2.RegisterTargetsInput) (*elbv2.RegisterTargetsOutput, error)
	ELBv2DeregisterTargets(ctx context.Context, input *elbv2.DeregisterTargetsInput) (*elbv2.DeregisterTargetsOutput, error)
}

type awsClient struct {
	ec2Client   *ec2.Client
	elbClient   *elb.Client
	elbv2Client *elbv2.Client
}

func (c *awsClient) DescribeDHCPOptions(ctx context.Context, input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
	return c.ec2Client.DescribeDhcpOptions(ctx, input)
}

func (c *awsClient) DescribeImages(ctx context.Context, input *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
	return c.ec2Client.DescribeImages(ctx, input)
}

func (c *awsClient) DescribeVpcs(ctx context.Context, input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	return c.ec2Client.DescribeVpcs(ctx, input)
}

func (c *awsClient) DescribeSubnets(ctx context.Context, input *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	return c.ec2Client.DescribeSubnets(ctx, input)
}

func (c *awsClient) DescribeAvailabilityZones(ctx context.Context, input *ec2.DescribeAvailabilityZonesInput) (*ec2.DescribeAvailabilityZonesOutput, error) {
	return c.ec2Client.DescribeAvailabilityZones(ctx, input)
}

func (c *awsClient) DescribeSecurityGroups(ctx context.Context, input *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	return c.ec2Client.DescribeSecurityGroups(ctx, input)
}

func (c *awsClient) DescribePlacementGroups(ctx context.Context, input *ec2.DescribePlacementGroupsInput) (*ec2.DescribePlacementGroupsOutput, error) {
	return c.ec2Client.DescribePlacementGroups(ctx, input)
}

func (c *awsClient) DescribeInstanceTypes(ctx context.Context, input *ec2.DescribeInstanceTypesInput) (*ec2.DescribeInstanceTypesOutput, error) {
	return c.ec2Client.DescribeInstanceTypes(ctx, input)
}

func (c *awsClient) DescribeHosts(ctx context.Context, input *ec2.DescribeHostsInput) (*ec2.DescribeHostsOutput, error) {
	return c.ec2Client.DescribeHosts(ctx, input)
}

func (c *awsClient) AllocateHosts(ctx context.Context, input *ec2.AllocateHostsInput) (*ec2.AllocateHostsOutput, error) {
	return c.ec2Client.AllocateHosts(ctx, input)
}

func (c *awsClient) ReleaseHosts(ctx context.Context, input *ec2.ReleaseHostsInput) (*ec2.ReleaseHostsOutput, error) {
	return c.ec2Client.ReleaseHosts(ctx, input)
}

func (c *awsClient) RunInstances(ctx context.Context, input *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
	return c.ec2Client.RunInstances(ctx, input)
}

func (c *awsClient) DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	return c.ec2Client.DescribeInstances(ctx, input)
}

func (c *awsClient) TerminateInstances(ctx context.Context, input *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
	return c.ec2Client.TerminateInstances(ctx, input)
}

func (c *awsClient) DescribeVolumes(ctx context.Context, input *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	return c.ec2Client.DescribeVolumes(ctx, input)
}

func (c *awsClient) CreateTags(ctx context.Context, input *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
	return c.ec2Client.CreateTags(ctx, input)
}

func (c *awsClient) CreatePlacementGroup(ctx context.Context, input *ec2.CreatePlacementGroupInput) (*ec2.CreatePlacementGroupOutput, error) {
	return c.ec2Client.CreatePlacementGroup(ctx, input)
}

func (c *awsClient) DeletePlacementGroup(ctx context.Context, input *ec2.DeletePlacementGroupInput) (*ec2.DeletePlacementGroupOutput, error) {
	return c.ec2Client.DeletePlacementGroup(ctx, input)
}

func (c *awsClient) RegisterInstancesWithLoadBalancer(ctx context.Context, input *elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error) {
	return c.elbClient.RegisterInstancesWithLoadBalancer(ctx, input)
}

func (c *awsClient) ELBv2DescribeLoadBalancers(ctx context.Context, input *elbv2.DescribeLoadBalancersInput) (*elbv2.DescribeLoadBalancersOutput, error) {
	return c.elbv2Client.DescribeLoadBalancers(ctx, input)
}

func (c *awsClient) ELBv2DescribeTargetGroups(ctx context.Context, input *elbv2.DescribeTargetGroupsInput) (*elbv2.DescribeTargetGroupsOutput, error) {
	return c.elbv2Client.DescribeTargetGroups(ctx, input)
}

func (c *awsClient) ELBv2DescribeTargetHealth(ctx context.Context, input *elbv2.DescribeTargetHealthInput) (*elbv2.DescribeTargetHealthOutput, error) {
	return c.elbv2Client.DescribeTargetHealth(ctx, input)
}

func (c *awsClient) ELBv2RegisterTargets(ctx context.Context, input *elbv2.RegisterTargetsInput) (*elbv2.RegisterTargetsOutput, error) {
	return c.elbv2Client.RegisterTargets(ctx, input)
}

func (c *awsClient) ELBv2DeregisterTargets(ctx context.Context, input *elbv2.DeregisterTargetsInput) (*elbv2.DeregisterTargetsOutput, error) {
	return c.elbv2Client.DeregisterTargets(ctx, input)
}

// NewClient creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use either the cluster AWS credentials
// secret if defined (i.e. in the root cluster),
// otherwise the IAM profile of the master where the actuator will run. (target clusters)
func NewClient(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (Client, error) {
	cfg, ec2OptFns, elbOptFns, elbv2OptFns, err := newAWSConfig(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	return &awsClient{
		ec2Client:   ec2.NewFromConfig(cfg, ec2OptFns...),
		elbClient:   elb.NewFromConfig(cfg, elbOptFns...),
		elbv2Client: elbv2.NewFromConfig(cfg, elbv2OptFns...),
	}, nil
}

// NewClientFromKeys creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use AWS credentials.
func NewClientFromKeys(accessKey, secretAccessKey, region string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretAccessKey, "")),
		awsconfig.WithAPIOptions([]func(*middleware.Stack) error{addUserAgentMiddleware}),
	)
	if err != nil {
		return nil, err
	}

	return &awsClient{
		ec2Client:   ec2.NewFromConfig(cfg),
		elbClient:   elb.NewFromConfig(cfg),
		elbv2Client: elbv2.NewFromConfig(cfg),
	}, nil
}

// DescribeRegionsData holds output of DescribeRegions API call and the time when it was last updated.
type DescribeRegionsData struct {
	err                   error
	describeRegionsOutput *ec2.DescribeRegionsOutput
	lastUpdated           time.Time
}

type regionCache struct {
	data  map[string]DescribeRegionsData
	mutex sync.RWMutex
}

// RegionCache caches successful DescribeRegions API calls.
type RegionCache interface {
	GetCachedDescribeRegions(ctx context.Context, cfg aws.Config) (*ec2.DescribeRegionsOutput, error)
}

// NewRegionCache creates a new empty DescribeRegionsData cache with lock.
func NewRegionCache() RegionCache {
	return &regionCache{
		data:  map[string]DescribeRegionsData{},
		mutex: sync.RWMutex{},
	}
}

// GetCachedDescribeRegions returns DescribeRegionsOutput from DescribeRegions AWS API call.
// It is cached to avoid AWS API calls on each reconcile loop.
func (c *regionCache) GetCachedDescribeRegions(ctx context.Context, cfg aws.Config) (*ec2.DescribeRegionsOutput, error) {
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()
	regionData := c.data[creds.AccessKeyID]
	if regionData.describeRegionsOutput != nil && regionData.err == nil &&
		time.Since(regionData.lastUpdated) < awsRegionsCacheExpirationDuration {
		klog.Info("Using cached AWS region data")
		return regionData.describeRegionsOutput, nil
	}

	// Use us-east-1 for describing regions
	cfgCopy := cfg.Copy()
	cfgCopy.Region = "us-east-1"
	ec2Client := ec2.NewFromConfig(cfgCopy)
	describeRegionsOutput, err := ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(true),
		DryRun:     aws.Bool(false),
	})
	if err != nil {
		regionData.err = err
		return nil, err
	}

	regionData.describeRegionsOutput = describeRegionsOutput
	regionData.lastUpdated = time.Now()
	c.data[creds.AccessKeyID] = regionData
	return describeRegionsOutput, nil
}

// Check that region is in the DescribeRegions list and is opted in.
func validateRegion(describeRegionsOutput *ec2.DescribeRegionsOutput, region string) (*ec2types.Region, error) {
	var regionData *ec2types.Region
	for i, currentRegion := range describeRegionsOutput.Regions {
		if aws.ToString(currentRegion.RegionName) == region {
			regionData = &describeRegionsOutput.Regions[i]
			break
		}
	}

	if regionData == nil {
		return nil, fmt.Errorf("region %s is not a valid region", region)
	}
	if aws.ToString(regionData.OptInStatus) == "not-opted-in" {
		return nil, fmt.Errorf("region %s is not opted in", region)
	}
	klog.Infof("AWS reports region %s is valid", region)
	return regionData, nil
}

// NewValidatedClient creates our client wrapper object for the actual AWS clients we use.
// This should behave the same as NewClient except it will validate the client configuration
// (eg the region) before returning the client.
func NewValidatedClient(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client, regionCache RegionCache) (Client, error) {
	cfg, ec2OptFns, elbOptFns, elbv2OptFns, err := newAWSConfig(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	// Validate the region by checking the DescribeRegions output
	klog.Infof("Validating region %s using API", region)
	describeRegionsOutput, err := regionCache.GetCachedDescribeRegions(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("could not retrieve region data: %w", err)
	}

	_, err = validateRegion(describeRegionsOutput, region)
	if err != nil {
		return nil, err
	}

	return &awsClient{
		ec2Client:   ec2.NewFromConfig(cfg, ec2OptFns...),
		elbClient:   elb.NewFromConfig(cfg, elbOptFns...),
		elbv2Client: elbv2.NewFromConfig(cfg, elbv2OptFns...),
	}, nil
}

func newAWSConfig(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (cfg aws.Config, ec2OptFns []func(*ec2.Options), elbOptFns []func(*elb.Options), elbv2OptFns []func(*elbv2.Options), err error) {
	configOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithAPIOptions([]func(*middleware.Stack) error{addUserAgentMiddleware}),
	}

	if secretName != "" {
		var secret corev1.Secret
		if err := ctrlRuntimeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: secretName}, &secret); err != nil {
			if apimachineryerrors.IsNotFound(err) {
				return aws.Config{}, nil, nil, nil, machineapiapierrors.InvalidMachineConfiguration("aws credentials secret %s/%s: %v not found", namespace, secretName, err)
			}
			return aws.Config{}, nil, nil, nil, err
		}
		sharedCredentialsFileMutex.Lock()
		defer sharedCredentialsFileMutex.Unlock()
		sharedCredsFile, err := sharedCredentialsFileFromSecret(&secret)
		if err != nil {
			return aws.Config{}, nil, nil, nil, fmt.Errorf("failed to create shared credentials file from Secret: %v", err)
		}

		// Ensure the file gets deleted in any case.
		defer func() {
			if removeErr := os.Remove(sharedCredsFile); removeErr != nil && err == nil {
				err = fmt.Errorf("failed to remove shared credentials file %s: %v", sharedCredsFile, removeErr)
			}
		}()

		configOpts = append(configOpts,
			awsconfig.WithSharedConfigFiles([]string{sharedCredsFile}),
			awsconfig.WithSharedCredentialsFiles([]string{sharedCredsFile}),
		)
	}

	// Resolve custom endpoints
	customEndpointsMap, err := resolveEndpointsMap(ctrlRuntimeClient)
	if err != nil {
		return aws.Config{}, nil, nil, nil, err
	}

	if len(customEndpointsMap) > 0 {
		if url, ok := customEndpointsMap["ec2"]; ok {
			ec2OptFns = append(ec2OptFns, func(o *ec2.Options) {
				o.BaseEndpoint = aws.String(url)
			})
		}
		if url, ok := customEndpointsMap["elasticloadbalancing"]; ok {
			elbOptFns = append(elbOptFns, func(o *elb.Options) {
				o.BaseEndpoint = aws.String(url)
			})
			elbv2OptFns = append(elbv2OptFns, func(o *elbv2.Options) {
				o.BaseEndpoint = aws.String(url)
			})
		}
	}

	// Handle custom CA bundle
	httpClient, err := buildHTTPClient(configManagedClient)
	if err != nil {
		return aws.Config{}, nil, nil, nil, fmt.Errorf("failed to set the custom CA bundle: %w", err)
	}
	if httpClient != nil {
		configOpts = append(configOpts, awsconfig.WithHTTPClient(httpClient))
	}

	// Load the config
	cfg, err = awsconfig.LoadDefaultConfig(context.Background(), configOpts...)
	if err != nil {
		return aws.Config{}, nil, nil, nil, err
	}

	return cfg, ec2OptFns, elbOptFns, elbv2OptFns, nil
}

// addUserAgentMiddleware adds provider version information to the user agent.
func addUserAgentMiddleware(stack *middleware.Stack) error {
	return stack.Build.Add(middleware.BuildMiddlewareFunc("UserAgent", func(
		ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler,
	) (middleware.BuildOutput, middleware.Metadata, error) {
		req, ok := in.Request.(*smithyhttp.Request)
		if ok {
			req.Header.Add("User-Agent", fmt.Sprintf("openshift.io/cluster-api-provider-aws %s", version.Version.String()))
		}
		return next.HandleBuild(ctx, in)
	}), middleware.After)
}

func resolveEndpointsMap(ctrlRuntimeClient client.Client) (map[string]string, error) {
	infra := &configv1.Infrastructure{}
	infraName := client.ObjectKey{Name: GlobalInfrastuctureName}

	if err := ctrlRuntimeClient.Get(context.Background(), infraName, infra); err != nil {
		return nil, err
	}

	// Do nothing when custom endpoints are missing
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return nil, nil
	}

	return buildCustomEndpointsMap(infra.Status.PlatformStatus.AWS.ServiceEndpoints), nil
}

// buildCustomEndpointsMap constructs a map that links endpoint name and it's url
func buildCustomEndpointsMap(customEndpoints []configv1.AWSServiceEndpoint) map[string]string {
	customEndpointsMap := make(map[string]string)

	for _, customEndpoint := range customEndpoints {
		customEndpointsMap[customEndpoint.Name] = customEndpoint.URL
	}

	return customEndpointsMap
}

// sharedCredentialsFileFromSecret returns a location (path) to the shared credentials
// file that was created using the provided secret
func sharedCredentialsFileFromSecret(secret *corev1.Secret) (filename string, err error) {
	var data []byte
	switch {
	case len(secret.Data["credentials"]) > 0:
		data = secret.Data["credentials"]
	case len(secret.Data["aws_access_key_id"]) > 0 && len(secret.Data["aws_secret_access_key"]) > 0:
		data = newConfigForStaticCreds(
			string(secret.Data["aws_access_key_id"]),
			string(secret.Data["aws_secret_access_key"]),
		)
	default:
		return "", fmt.Errorf("invalid secret for aws credentials")
	}

	// Re-using the same file every time to prevent leakage of memory to slab.
	// Related issue: https://issues.redhat.com/browse/RHEL-119532
	f, err := os.Create(sharedCredentialsFileName)
	if err != nil {
		return "", fmt.Errorf("failed to create file for shared credentials: %v", err)
	}

	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close file %s: %v", f.Name(), closeErr)
		}
	}()

	if _, err = f.Write(data); err != nil {
		// Delete the file in case of having an error. Otherwise the calling function needs to handle deletion.
		if deleteErr := os.Remove(f.Name()); deleteErr != nil {
			return "", fmt.Errorf("failed to write credentials to %s and delete it afterwards: %v, %v", f.Name(), err, deleteErr)
		}
		return "", fmt.Errorf("failed to write credentials to %s: %v", f.Name(), err)
	}
	return f.Name(), nil
}

func newConfigForStaticCreds(accessKey string, accessSecret string) []byte {
	buf := &bytes.Buffer{}
	fmt.Fprint(buf, "[default]\n")
	fmt.Fprintf(buf, "aws_access_key_id = %s\n", accessKey)
	fmt.Fprintf(buf, "aws_secret_access_key = %s\n", accessSecret)
	return buf.Bytes()
}

// buildHTTPClient creates an HTTP client with custom CA bundle if configured.
// Returns nil if no custom CA bundle is configured.
func buildHTTPClient(configManagedClient client.Client) (*http.Client, error) {
	cm := &corev1.ConfigMap{}
	switch err := configManagedClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: KubeCloudConfigNamespace, Name: kubeCloudConfigName},
		cm,
	); {
	case apimachineryerrors.IsNotFound(err):
		// no cloud config ConfigMap, so no custom CA bundle
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("failed to get kube-cloud-config ConfigMap: %w", err)
	}
	caBundle, ok := cm.Data[cloudCABundleKey]
	if !ok {
		// no "ca-bundle.pem" key in the ConfigMap, so no custom CA bundle
		return nil, nil
	}
	klog.Info("using a custom CA bundle")

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM([]byte(caBundle)) {
		return nil, fmt.Errorf("failed to parse custom CA bundle")
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    certPool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}, nil
}
