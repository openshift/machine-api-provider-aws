package machine

import (
	"context"
	"errors"
	"fmt"

	errorutil "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	smithy "github.com/aws/smithy-go"

	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
)

func registerWithClassicLoadBalancers(client awsclient.Client, names []string, instance ec2types.Instance) error {
	klog.V(4).Infof("Updating classic load balancer registration for %q", *instance.InstanceId)
	elbInstance := elbtypes.Instance{InstanceId: instance.InstanceId}
	var errs []error
	for _, elbName := range names {
		req := &elb.RegisterInstancesWithLoadBalancerInput{
			Instances:        []elbtypes.Instance{elbInstance},
			LoadBalancerName: aws.String(elbName),
		}
		_, err := client.RegisterInstancesWithLoadBalancer(context.TODO(), req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", elbName, err))
		}
	}

	if len(errs) > 0 {
		return errorutil.NewAggregate(errs)
	}
	return nil
}

func registerWithNetworkLoadBalancers(client awsclient.Client, names []string, instance ec2types.Instance) error {
	klog.V(4).Infof("Updating network load balancer registration for %q", *instance.InstanceId)
	targetGroups, err := gatherLoadBalancerTargetGroups(client, names)
	if err != nil {
		return err
	}

	errs := []error{}
	for _, targetGroup := range targetGroups {

		var target elbv2types.TargetDescription
		switch string(targetGroup.TargetType) {
		case string(elbv2types.TargetTypeEnumInstance):
			target = elbv2types.TargetDescription{
				Id: instance.InstanceId,
			}
			klog.V(4).Infof("Registering instance %q by instance ID to target group: %v", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn))
		case string(elbv2types.TargetTypeEnumIp):
			target = elbv2types.TargetDescription{
				Id: instance.PrivateIpAddress,
			}
			klog.V(4).Infof("Registering instance %q by IP to target group: %v", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn))
		}

		registeredTargets, err := gatherLoadBalancerTargetGroupRegisteredTargets(client, targetGroup.TargetGroupArn)
		if err != nil {
			klog.Errorf("Failed to gather registered targets for target group %q: %v", aws.ToString(targetGroup.TargetGroupArn), err)
			errs = append(errs, fmt.Errorf("%s: %v", aws.ToString(targetGroup.TargetGroupArn), err))
		}
		if registeredTargets != nil {
			if _, ok := registeredTargets[*target.Id]; ok {
				klog.V(4).Infof("Skipping registration for instance %q to target group %q: Instance already registered", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn))
				continue
			}
		}

		registerTargetsInput := &elbv2.RegisterTargetsInput{
			TargetGroupArn: targetGroup.TargetGroupArn,
			Targets:        []elbv2types.TargetDescription{target},
		}
		if _, err := client.ELBv2RegisterTargets(context.TODO(), registerTargetsInput); err != nil {
			klog.Errorf("Failed to register instance %q with target group %q: %v", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn), err)
			errs = append(errs, fmt.Errorf("%s: %v", aws.ToString(targetGroup.TargetGroupArn), err))
		}
	}
	if len(errs) > 0 {
		return errorutil.NewAggregate(errs)
	}
	return nil
}

// deregisterNetworkLoadBalancers serves manual instance removal from Network LoadBalancer TargetGroup list
// for the instances attached by IP. Unlike instance reference, IP attachment should be cleaned manually.
func deregisterNetworkLoadBalancers(client awsclient.Client, names []string, instance ec2types.Instance) error {
	if instance.PrivateIpAddress == nil {
		klog.V(4).Infof("Instance %q does not have private ip, skipping...", *instance.InstanceId)
		return nil
	}

	klog.V(4).Infof("Removing network load balancer registration for %q", *instance.InstanceId)
	targetGroupsOutput, err := gatherLoadBalancerTargetGroups(client, names)
	if err != nil {
		return err
	}

	filteredGroupsByIP := []elbv2types.TargetGroup{}
	for _, targetGroup := range targetGroupsOutput {
		if string(targetGroup.TargetType) == string(elbv2types.TargetTypeEnumIp) {
			filteredGroupsByIP = append(filteredGroupsByIP, targetGroup)
		}
	}

	errs := []error{}
	for _, targetGroup := range filteredGroupsByIP {
		klog.V(4).Infof("Unregistering instance %q registered by ip from target group: %v", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn))

		deregisterTargetsInput := &elbv2.DeregisterTargetsInput{
			TargetGroupArn: targetGroup.TargetGroupArn,
			Targets: []elbv2types.TargetDescription{{
				Id: instance.PrivateIpAddress,
			}},
		}
		_, err := client.ELBv2DeregisterTargets(context.TODO(), deregisterTargetsInput)
		if err != nil {
			var ae smithy.APIError
			if errors.As(err, &ae) {
				switch ae.ErrorCode() {
				case "InvalidTarget", "TargetGroupNotFound":
					// Ignoring error when LB target group was already removed
					continue
				}
			}
			klog.Errorf("Failed to unregister instance %q from target group %q: %v", *instance.InstanceId, aws.ToString(targetGroup.TargetGroupArn), err)
			errs = append(errs, fmt.Errorf("%s: %v", aws.ToString(targetGroup.TargetGroupArn), err))
		}
	}
	if len(errs) > 0 {
		return errorutil.NewAggregate(errs)
	}
	return nil
}

func gatherLoadBalancerTargetGroups(client awsclient.Client, names []string) ([]elbv2types.TargetGroup, error) {
	lbNames := make([]string, len(names))
	copy(lbNames, names)
	lbsRequest := &elbv2.DescribeLoadBalancersInput{
		Names: lbNames,
	}
	lbsResponse, err := client.ELBv2DescribeLoadBalancers(context.TODO(), lbsRequest)
	if err != nil {
		klog.Errorf("Failed to describe load balancers %v: %v", names, err)
		return nil, err
	}
	// Use a map for target groups to get unique target group entries across load balancers
	targetGroups := []elbv2types.TargetGroup{}
	for _, loadBalancer := range lbsResponse.LoadBalancers {
		klog.V(4).Infof("Retrieving target groups for load balancer %s", aws.ToString(loadBalancer.LoadBalancerName))
		targetGroupsInput := &elbv2.DescribeTargetGroupsInput{
			LoadBalancerArn: loadBalancer.LoadBalancerArn,
		}
		targetGroupsOutput, err := client.ELBv2DescribeTargetGroups(context.TODO(), targetGroupsInput)
		if err != nil {
			klog.Errorf("Failed to retrieve load balancer target groups for %q: %v", aws.ToString(loadBalancer.LoadBalancerName), err)
			return nil, err
		}
		targetGroups = append(targetGroups, targetGroupsOutput.TargetGroups...)
	}

	return targetGroups, nil
}

// gatherLoadBalancerTargetGroupRegisteredTargets looks for all targets that are registered to a particular targetGroup.
// Within the AWS API, the only way to find the targets that are registered is to look at the target health for the group.
// The target health response contains all of the targets and importantly, their IDs which we need later to compare with
// the target ID we are wanting to register.
func gatherLoadBalancerTargetGroupRegisteredTargets(client awsclient.Client, targetGroupArn *string) (map[string]struct{}, error) {
	targetHealthRequest := &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: targetGroupArn,
	}
	targetHealthResponse, err := client.ELBv2DescribeTargetHealth(context.TODO(), targetHealthRequest)
	if err != nil {
		klog.Errorf("Failed to describe target health: %v", err)
		return nil, err
	}

	targetIDs := make(map[string]struct{})
	for _, targetHealth := range targetHealthResponse.TargetHealthDescriptions {
		targetIDs[aws.ToString(targetHealth.Target.Id)] = struct{}{}
	}
	return targetIDs, nil
}
