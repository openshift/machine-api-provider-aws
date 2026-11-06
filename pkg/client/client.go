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
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/openshift/machine-api-provider-aws/pkg/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/smithy-go/middleware"
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

	RegisterInstancesWithLoadBalancer(ctx context.Context, input *elasticloadbalancing.RegisterInstancesWithLoadBalancerInput) (*elasticloadbalancing.RegisterInstancesWithLoadBalancerOutput, error)
	ELBv2DescribeLoadBalancers(ctx context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	ELBv2DescribeTargetGroups(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	ELBv2DescribeTargetHealth(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
	ELBv2RegisterTargets(ctx context.Context, input *elasticloadbalancingv2.RegisterTargetsInput) (*elasticloadbalancingv2.RegisterTargetsOutput, error)
	ELBv2DeregisterTargets(ctx context.Context, input *elasticloadbalancingv2.DeregisterTargetsInput) (*elasticloadbalancingv2.DeregisterTargetsOutput, error)
}

type awsClient struct {
	ec2Client   *ec2.Client
	elbClient   *elasticloadbalancing.Client
	elbv2Client *elasticloadbalancingv2.Client
	cfg         aws.Config
}

func (c *awsClient) CloseIdleConnections() {
	httpClient := c.cfg.HTTPClient
	if httpClient == nil {
		klog.V(4).Info("No HTTP client configured, skipping idle connection cleanup")
		return
	}
	if closer, ok := httpClient.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
		klog.V(4).Info("Closed AWS config idle connections")
	}
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

func (c *awsClient) RegisterInstancesWithLoadBalancer(ctx context.Context, input *elasticloadbalancing.RegisterInstancesWithLoadBalancerInput) (*elasticloadbalancing.RegisterInstancesWithLoadBalancerOutput, error) {
	return c.elbClient.RegisterInstancesWithLoadBalancer(ctx, input)
}

func (c *awsClient) ELBv2DescribeLoadBalancers(ctx context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return c.elbv2Client.DescribeLoadBalancers(ctx, input)
}

func (c *awsClient) ELBv2DescribeTargetGroups(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	return c.elbv2Client.DescribeTargetGroups(ctx, input)
}

func (c *awsClient) ELBv2DescribeTargetHealth(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	return c.elbv2Client.DescribeTargetHealth(ctx, input)
}

func (c *awsClient) ELBv2RegisterTargets(ctx context.Context, input *elasticloadbalancingv2.RegisterTargetsInput) (*elasticloadbalancingv2.RegisterTargetsOutput, error) {
	return c.elbv2Client.RegisterTargets(ctx, input)
}

func (c *awsClient) ELBv2DeregisterTargets(ctx context.Context, input *elasticloadbalancingv2.DeregisterTargetsInput) (*elasticloadbalancingv2.DeregisterTargetsOutput, error) {
	return c.elbv2Client.DeregisterTargets(ctx, input)
}

// NewClient creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use either the cluster AWS credentials
// secret if defined (i.e. in the root cluster),
// otherwise the IAM profile of the master where the actuator will run. (target clusters)
func NewClient(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (Client, error) {
	cfg, customEndpoints, err := newAWSConfig(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	return &awsClient{
		ec2Client:   ec2ClientFromConfig(cfg, customEndpoints),
		elbClient:   elbClientFromConfig(cfg, customEndpoints),
		elbv2Client: elbv2ClientFromConfig(cfg, customEndpoints),
		cfg:         cfg,
	}, nil
}

// NewClientFromKeys creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use AWS credentials.
func NewClientFromKeys(accessKey, secretAccessKey, region string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretAccessKey, "")),
	)
	if err != nil {
		return nil, err
	}

	cfg.APIOptions = append(cfg.APIOptions, addUserAgentMiddleware())

	return &awsClient{
		ec2Client:   ec2.NewFromConfig(cfg),
		elbClient:   elasticloadbalancing.NewFromConfig(cfg),
		elbv2Client: elasticloadbalancingv2.NewFromConfig(cfg),
		cfg:         cfg,
	}, nil
}

// DescribeRegionsData holds output of DescribeRegions API call and the time when it was last updated.
type DescribeRegionsData struct {
	err                   error
	describeRegionsOutput *ec2.DescribeRegionsOutput
	validatedRegions      map[string]bool
	lastUpdated           time.Time
}

type regionCache struct {
	data  map[string]DescribeRegionsData
	mutex sync.RWMutex
}

// RegionCache caches successful DescribeRegions API calls and region validation results.
type RegionCache interface {
	GetCachedDescribeRegions(ctx context.Context, cfg aws.Config) (*ec2.DescribeRegionsOutput, error)
	IsRegionValidated(ctx context.Context, cfg aws.Config, region string) (bool, error)
	SetRegionValidated(ctx context.Context, cfg aws.Config, region string) error
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

	// Use default region to send our request
	cfgForRegions := cfg
	cfgForRegions.Region = "us-east-1"
	ec2Client := ec2.NewFromConfig(cfgForRegions)
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

func (c *regionCache) IsRegionValidated(ctx context.Context, cfg aws.Config, region string) (bool, error) {
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return false, err
	}

	c.mutex.RLock()
	defer c.mutex.RUnlock()
	regionData := c.data[creds.AccessKeyID]
	if regionData.validatedRegions != nil &&
		regionData.validatedRegions[region] &&
		time.Since(regionData.lastUpdated) < awsRegionsCacheExpirationDuration {
		klog.V(4).Infof("Region %s validation cached, skipping endpoint check", region)
		return true, nil
	}
	return false, nil
}

func (c *regionCache) SetRegionValidated(ctx context.Context, cfg aws.Config, region string) error {
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()
	regionData := c.data[creds.AccessKeyID]
	if regionData.validatedRegions == nil {
		regionData.validatedRegions = make(map[string]bool)
	}
	regionData.validatedRegions[region] = true
	if regionData.lastUpdated.IsZero() {
		regionData.lastUpdated = time.Now()
	}
	c.data[creds.AccessKeyID] = regionData
	return nil
}

// Check that region is in the DescribeRegions list and is opted in.
func validateRegion(describeRegionsOutput *ec2.DescribeRegionsOutput, region string) (*ec2types.Region, error) {
	var regionData *ec2types.Region
	for i := range describeRegionsOutput.Regions {
		if aws.ToString(describeRegionsOutput.Regions[i].RegionName) == region {
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
	cfg, customEndpoints, err := newAWSConfig(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	validated, err := regionCache.IsRegionValidated(ctx, cfg, region)
	if err != nil {
		return nil, fmt.Errorf("could not check region validation cache: %w", err)
	}

	if !validated {
		// In SDK v2, endpoint resolution is permissive and resolves any region.
		// We always validate via the DescribeRegions API to ensure the region is real and opted in.
		klog.Infof("Region %s is not cached, validating using API", region)
		var describeRegionsOutput *ec2.DescribeRegionsOutput
		describeRegionsOutput, err = regionCache.GetCachedDescribeRegions(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("could not retrieve region data: %w", err)
		}

		_, err = validateRegion(describeRegionsOutput, region)
		if err != nil {
			return nil, err
		}
		klog.V(4).Infof("Region %s validated via API, caching result", region)
		if cacheErr := regionCache.SetRegionValidated(ctx, cfg, region); cacheErr != nil {
			klog.V(4).Infof("Failed to cache region validation for %s: %v", region, cacheErr)
		}
	}

	return &awsClient{
		ec2Client:   ec2ClientFromConfig(cfg, customEndpoints),
		elbClient:   elbClientFromConfig(cfg, customEndpoints),
		elbv2Client: elbv2ClientFromConfig(cfg, customEndpoints),
		cfg:         cfg,
	}, nil
}

func newAWSConfig(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (cfg aws.Config, customEndpoints map[string]string, err error) {
	optFns := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}

	if secretName != "" {
		var secret corev1.Secret
		if err := ctrlRuntimeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: secretName}, &secret); err != nil {
			if apimachineryerrors.IsNotFound(err) {
				return aws.Config{}, nil, machineapiapierrors.InvalidMachineConfiguration("aws credentials secret %s/%s: %v not found", namespace, secretName, err)
			}
			return aws.Config{}, nil, err
		}
		sharedCredentialsFileMutex.Lock()
		defer sharedCredentialsFileMutex.Unlock()
		sharedCredsFile, err := sharedCredentialsFileFromSecret(&secret)
		if err != nil {
			return aws.Config{}, nil, fmt.Errorf("failed to create shared credentials file from Secret: %v", err)
		}

		// Ensure the file gets deleted in any case.
		defer func() {
			if removeErr := os.Remove(sharedCredsFile); removeErr != nil && err == nil {
				err = fmt.Errorf("failed to remove shared credentials file %s: %v", sharedCredsFile, removeErr)
			}
		}()

		optFns = append(optFns,
			awsconfig.WithSharedConfigProfile("default"),
			awsconfig.WithSharedCredentialsFiles([]string{sharedCredsFile}),
		)
	}

	if err := addCustomCABundle(&optFns, configManagedClient); err != nil {
		return aws.Config{}, nil, fmt.Errorf("failed to set the custom CA bundle: %w", err)
	}

	// Otherwise default to relying on the IAM role of the masters where the actuator is running:
	cfg, err = awsconfig.LoadDefaultConfig(context.Background(), optFns...)
	if err != nil {
		return aws.Config{}, nil, err
	}

	cfg.APIOptions = append(cfg.APIOptions, addUserAgentMiddleware())

	// Resolve custom endpoints
	customEndpoints, err = getCustomEndpoints(ctrlRuntimeClient)
	if err != nil {
		return aws.Config{}, nil, err
	}

	return cfg, customEndpoints, nil
}

// addUserAgentMiddleware returns a middleware function that adds cluster-api-provider-aws
// version information to requests made by the AWS SDK.
func addUserAgentMiddleware() func(*middleware.Stack) error {
	return awsmiddleware.AddUserAgentKeyValue("openshift.io/cluster-api-provider-aws", version.Version.String())
}

// getCustomEndpoints returns custom endpoint URLs from the Infrastructure object
func getCustomEndpoints(ctrlRuntimeClient client.Client) (map[string]string, error) {
	infra := &configv1.Infrastructure{}
	infraName := client.ObjectKey{Name: GlobalInfrastuctureName}

	if err := ctrlRuntimeClient.Get(context.Background(), infraName, infra); err != nil {
		return nil, err
	}

	// Do nothing when custom endpoints are missing
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return nil, nil
	}

	customEndpointsMap := buildCustomEndpointsMap(infra.Status.PlatformStatus.AWS.ServiceEndpoints)

	if len(customEndpointsMap) == 0 {
		return nil, nil
	}

	return customEndpointsMap, nil
}

// ec2ClientFromConfig creates an EC2 client with optional custom endpoint
func ec2ClientFromConfig(cfg aws.Config, customEndpoints map[string]string) *ec2.Client {
	opts := []func(*ec2.Options){}
	if url, ok := customEndpoints["ec2"]; ok {
		opts = append(opts, func(o *ec2.Options) {
			o.BaseEndpoint = aws.String(url)
		})
	}
	return ec2.NewFromConfig(cfg, opts...)
}

// elbClientFromConfig creates an ELB Classic client with optional custom endpoint
func elbClientFromConfig(cfg aws.Config, customEndpoints map[string]string) *elasticloadbalancing.Client {
	opts := []func(*elasticloadbalancing.Options){}
	if url, ok := customEndpoints["elasticloadbalancing"]; ok {
		opts = append(opts, func(o *elasticloadbalancing.Options) {
			o.BaseEndpoint = aws.String(url)
		})
	}
	return elasticloadbalancing.NewFromConfig(cfg, opts...)
}

// elbv2ClientFromConfig creates an ELBv2 client with optional custom endpoint.
// If no elasticloadbalancingv2 endpoint is configured, it falls back to the
// elasticloadbalancing endpoint since both services often share the same
// custom endpoint in restricted environments (e.g., GovCloud, C2S).
func elbv2ClientFromConfig(cfg aws.Config, customEndpoints map[string]string) *elasticloadbalancingv2.Client {
	opts := []func(*elasticloadbalancingv2.Options){}
	if url, ok := customEndpoints["elasticloadbalancingv2"]; ok {
		opts = append(opts, func(o *elasticloadbalancingv2.Options) {
			o.BaseEndpoint = aws.String(url)
		})
	} else if url, ok := customEndpoints["elasticloadbalancing"]; ok {
		opts = append(opts, func(o *elasticloadbalancingv2.Options) {
			o.BaseEndpoint = aws.String(url)
		})
	}
	return elasticloadbalancingv2.NewFromConfig(cfg, opts...)
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

// addCustomCABundle will set up a custom CA bundle in the AWS options if a CA bundle is configured in the
// kube cloud config.
func addCustomCABundle(optFns *[]func(*awsconfig.LoadOptions) error, configManagedClient client.Client) error {
	cm := &corev1.ConfigMap{}
	switch err := configManagedClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: KubeCloudConfigNamespace, Name: kubeCloudConfigName},
		cm,
	); {
	case apimachineryerrors.IsNotFound(err):
		// no cloud config ConfigMap, so no custom CA bundle
		return nil
	case err != nil:
		return fmt.Errorf("failed to get kube-cloud-config ConfigMap: %w", err)
	}
	caBundle, ok := cm.Data[cloudCABundleKey]
	if !ok {
		// no "ca-bundle.pem" key in the ConfigMap, so no custom CA bundle
		return nil
	}
	klog.Info("using a custom CA bundle")
	*optFns = append(*optFns, awsconfig.WithCustomCABundle(strings.NewReader(caBundle)))
	return nil
}
