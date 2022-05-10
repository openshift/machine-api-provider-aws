package awsplacementgroup

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machinev1 "github.com/openshift/machine-api-provider-aws/pkg/api/machine/v1"
	awsclient "github.com/openshift/machine-api-provider-aws/pkg/client"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// awsPlacementGroupFinalizer is finalizer string for AWSPlacementGroup objects.
const awsPlacementGroupFinalizer = "awsplacementgroup.machine.openshift.io"

const (
	// readyConditionType indicates placement group condition type.
	readyConditionType string = "Ready"
	// creationSucceededConditionReason indicates placement group creation success.
	creationSucceededConditionReason string = "CreationSucceeded"
	// creationFailedConditionReason indicates placement group creation failure.
	creationFailedConditionReason string = "CreationFailed"
	// deletionFailedConditionReason indicates placement group creation failure.
	deletionFailedConditionReason string = "DeletionFailed"
	// configurationMismatchConditionReason indicates placement group configuration doesn't match the real-world resource configuration.
	configurationMismatchConditionReason string = "ConfigurationMismatch"
	// configurationInSyncConditionReason indicates placement group configuration is in sync with configuration.
	configurationInSyncConditionReason string = "ConfigurationInSync"
)

// Reconciler reconciles AWSPlacementGroup.
type Reconciler struct {
	Client              client.Client
	Log                 logr.Logger
	AWSClientBuilder    awsclient.AwsClientBuilderFuncType
	ConfigManagedClient client.Client

	regionCache awsclient.RegionCache
	recorder    record.EventRecorder
	scheme      *runtime.Scheme
}

// SetupWithManager creates a new controller for a manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&machinev1.AWSPlacementGroup{}).
		// TODO(damdo): uncomment when Machine's ProviderSpec supports Groups
		// Watches(&source.Kind{Type: &machinev1beta1.Machine{}}, handler.EnqueueRequestsFromMapFunc(machineToAWSPlacementGroup(r))).
		WithOptions(options).
		Complete(r); err != nil {
		return fmt.Errorf("failed setting up with a controller manager: %w", err)
	}

	r.recorder = mgr.GetEventRecorderFor("awsplacementgroup-controller")
	r.scheme = mgr.GetScheme()

	return nil
}

// Reconcile implements controller runtime Reconciler interface.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(3).Info("Reconciling aws placement group")

	awsPlacementGroup := &machinev1.AWSPlacementGroup{}
	if err := r.Client.Get(ctx, req.NamespacedName, awsPlacementGroup); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found.
			// Return without error to avoid requeue as created objects are automatically garbage collected.
			return ctrl.Result{}, nil
		}
		// For any other type of error, requeue immediately.
		return ctrl.Result{}, err
	}

	if err := validateAWSPlacementGroup(awsPlacementGroup); err != nil {
		logger.Error(err, "aws placement group failed validation")
		// Return without erroring to avoid requeue.
		// The object shouldn't be requeued until it has been modified and is ready to be validated again,
		// as it is invalid.
		return ctrl.Result{}, nil
	}

	// Get the Infrastructure object.
	infra := &configv1.Infrastructure{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: awsclient.GlobalInfrastuctureName}, infra); err != nil {
		return ctrl.Result{}, fmt.Errorf("could not fetch infrastructure object: %w", err)
	}

	// Check if the CredentialsSecret is defined,
	// then obtain its name for later use.
	credentialsSecretName := ""
	if awsPlacementGroup.Spec.CredentialsSecret != nil {
		credentialsSecretName = awsPlacementGroup.Spec.CredentialsSecret.Name
	}

	awsClient, err := r.AWSClientBuilder(
		r.Client, credentialsSecretName, awsPlacementGroup.Namespace,
		infra.Status.PlatformStatus.AWS.Region, r.ConfigManagedClient, r.regionCache)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("could not create aws client: %w", err)
	}

	originalAWSPlacementGroupToPatch := client.MergeFrom(awsPlacementGroup.DeepCopy())

	result, err := r.reconcile(ctx, awsClient, logger, infra, awsPlacementGroup)
	if err != nil {
		// Don't return here. To later attempt to Patch the AWSPlacementGroup regardless of an error.
		logger.Error(err, "failed to reconcile aws placement group")
		r.recorder.Eventf(awsPlacementGroup, corev1.EventTypeWarning, "ReconcileError", "%v", err)
	}

	originalAWSPlacementGroupStatus := awsPlacementGroup.Status.DeepCopy()

	if err := r.Client.Patch(ctx, awsPlacementGroup, originalAWSPlacementGroupToPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch aws placement group: %w", err)
	}

	awsPlacementGroup.Status = *originalAWSPlacementGroupStatus

	if err := r.Client.Status().Patch(ctx, awsPlacementGroup, originalAWSPlacementGroupToPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch aws placement group status: %w", err)
	}

	return result, err
}

// reconcile reconciles an AWSPlacementGroup.
func (r *Reconciler) reconcile(ctx context.Context, awsClient awsclient.Client,
	logger logr.Logger, infra *configv1.Infrastructure, awsPlacementGroup *machinev1.AWSPlacementGroup) (ctrl.Result, error) {
	now := metav1.Now()
	if awsPlacementGroup.Status.ExpiresAt == nil ||
		awsPlacementGroup.Status.ExpiresAt.Before(&now) {
		// The cached ObservedConfiguration stored in the AWSPlacementGroup Status
		// is expired or not present. Proceed with the syncing.
		// Check AWS for the configuration of the placement group and reflect this in the status of the object.
		if err := reflectObservedConfiguration(awsClient, logger, awsPlacementGroup); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reflect observed configuration in status: %w", err)
		}
	}

	// Update Status.ManagementState with the observed spec value.
	awsPlacementGroup.Status.ManagementState = awsPlacementGroup.Spec.ManagementSpec.ManagementState

	// If the placement group is Unmanaged, cleanup and return.
	if awsPlacementGroup.Spec.ManagementSpec.ManagementState == machinev1.UnmanagedManagementState {
		// This AWSPlacementGroup is now Unmanaged so clean up any machine finalizer if there is any
		// as placement group resources on AWS shouldn't be deleted if they are running in Unmanaged mode.
		if controllerutil.ContainsFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer) {
			controllerutil.RemoveFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer)
			logger.Info("removing finalizer from aws placement group")

			return ctrl.Result{Requeue: true}, nil
		}

		logger.Info("ignoring unmanaged aws placement group")
		// Return and requeue the placement group even if it is Unmanaged to keep syncing up its ObservedConfiguration.
		return ctrl.Result{RequeueAfter: requeueAt(awsPlacementGroup.Status.ExpiresAt.Time)}, nil
	}

	// If object DeletionTimestamp is zero, it means the object is not being deleted
	// so clean up relevant resources.
	if awsPlacementGroup.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer) {
			controllerutil.AddFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer)
			logger.Info("adding finalizer to aws placement group")

			return ctrl.Result{Requeue: true}, nil
		}
	}

	// If object DeletionTimestamp is not zero, it means the object is being deleted
	// so clean up relevant resources.
	if !awsPlacementGroup.DeletionTimestamp.IsZero() {
		// No-op if the finalizer has been removed.
		if !controllerutil.ContainsFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer) {
			logger.Info("reconciling aws placement group results in a no-op as there is no finalizer")
			return ctrl.Result{}, nil
		}

		logger.Info("reconciling aws placement group triggers deletion")

		if err := deletePlacementGroup(awsClient, logger, awsPlacementGroup, infra); err != nil {
			werr := fmt.Errorf("failed to delete aws placement group: %w", err)
			meta.SetStatusCondition(&awsPlacementGroup.Status.Conditions, metav1.Condition{
				Type:    "Deleting",
				Status:  metav1.ConditionTrue,
				Message: werr.Error(),
				Reason:  deletionFailedConditionReason,
			})

			return ctrl.Result{}, werr
		}

		// Remove finalizer on successful deletion.
		controllerutil.RemoveFinalizer(awsPlacementGroup, awsPlacementGroupFinalizer)
		logger.Info("removing finalizer after successful aws placement group deletion")

		return ctrl.Result{}, nil
	}

	// Conditionally create or check the placement group.
	if err := checkOrCreatePlacementGroup(awsClient, logger, awsPlacementGroup, infra); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAt(awsPlacementGroup.Status.ExpiresAt.Time)}, nil
}

// mergeInfrastructureAndAWSPlacementGroupSpecTags merge list of tags from AWSPlacementGroup provider spec and Infrastructure object platform spec.
// Machine tags have precedence over Infrastructure.
func mergeInfrastructureAndAWSPlacementGroupSpecTags(awsPlacementGroupSpecTags []machinev1beta1.TagSpecification, infra *configv1.Infrastructure) []machinev1beta1.TagSpecification {
	if infra == nil || infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || infra.Status.PlatformStatus.AWS.ResourceTags == nil {
		return awsPlacementGroupSpecTags
	}

	mergedList := []machinev1beta1.TagSpecification{}
	mergedList = append(mergedList, awsPlacementGroupSpecTags...)

	for _, tag := range infra.Status.PlatformStatus.AWS.ResourceTags {
		mergedList = append(mergedList, machinev1beta1.TagSpecification{Name: tag.Key, Value: tag.Value})
	}

	return mergedList
}

// buildPlacementGroupTagList compile a list of ec2 tags from AWSPlacementGroup provider spec and infrastructure object platform spec.
func buildPlacementGroupTagList(awsPlacementGroup string, awsPlacementGroupSpecTags []machinev1beta1.TagSpecification, infra *configv1.Infrastructure) []*ec2.Tag {
	rawTagList := []*ec2.Tag{}
	mergedTags := mergeInfrastructureAndAWSPlacementGroupSpecTags(awsPlacementGroupSpecTags, infra)
	clusterID := infra.Status.InfrastructureName

	for _, tag := range mergedTags {
		// AWS tags are case sensitive, so we don't need to worry about other casing of "Name"
		if !strings.HasPrefix(tag.Name, "kubernetes.io/cluster/") && tag.Name != "Name" {
			rawTagList = append(rawTagList, &ec2.Tag{Key: aws.String(tag.Name), Value: aws.String(tag.Value)})
		}
	}

	rawTagList = append(rawTagList, []*ec2.Tag{
		{Key: aws.String("kubernetes.io/cluster/" + clusterID), Value: aws.String("owned")},
		{Key: aws.String("Name"), Value: aws.String(awsPlacementGroup)},
	}...)

	return removeDuplicatedTags(rawTagList)
}

// removeDuplicatedTags scan machine tags, and return a deduped tags list. The first found value gets precedence.
func removeDuplicatedTags(tags []*ec2.Tag) []*ec2.Tag {
	m := make(map[string]bool)
	result := []*ec2.Tag{}

	// look for duplicates
	for _, entry := range tags {
		if _, value := m[*entry.Key]; !value {
			m[*entry.Key] = true

			result = append(result, entry)
		}
	}

	return result
}

// isAWS4xxError will determine if the passed error is an AWS error with a 4xx status code.
func isAWS4xxError(err error) bool {
	if _, ok := err.(awserr.Error); ok {
		if reqErr, ok := err.(awserr.RequestFailure); ok {
			if reqErr.StatusCode() >= 400 && reqErr.StatusCode() < 500 {
				return true
			}
		}
	}

	return false
}

// checkOrCreatePlacementGroup checks for the existence of a placement group on AWS and validates its config
// it proceeds to create one if such group doesn't exist.
func checkOrCreatePlacementGroup(client awsclient.Client, logger logr.Logger, pg *machinev1.AWSPlacementGroup, infra *configv1.Infrastructure) error {
	placementGroups, err := client.DescribePlacementGroups(&ec2.DescribePlacementGroupsInput{
		GroupNames: []*string{aws.String(pg.Name)},
	})
	if err != nil && !isAWS4xxError(err) {
		// Ignore a 400 error as AWS will report an unknown placement group as a 400.
		return fmt.Errorf("failed to check aws placement group: could not describe aws placement groups: %w", err)
	}

	// More than one placement group matching.
	if len(placementGroups.PlacementGroups) > 1 {
		return fmt.Errorf("failed to check aws placement group: expected 1 aws placement group for name %q, got %d", pg.Name, len(placementGroups.PlacementGroups))
	}

	// Placement group already exists on AWS.
	if len(placementGroups.PlacementGroups) == 1 {
		// Validate its configuration.
		if err := validateExistingPlacementGroupConfig(pg, placementGroups.PlacementGroups[0]); err != nil {
			werr := fmt.Errorf("invalid configuration for existing aws placement group: %w", err)
			// Set the Ready Condition to False due to a Configuration Mismatch.
			meta.SetStatusCondition(&pg.Status.Conditions, metav1.Condition{
				Type:    readyConditionType,
				Status:  metav1.ConditionFalse,
				Reason:  configurationMismatchConditionReason,
				Message: werr.Error(),
			})

			return werr
		}

		setObservedConfiguration(pg, placementGroups.PlacementGroups[0])

		return nil
	}

	// Build a tag list for the placement group by inheriting user defined tags from infra.
	tagList := buildPlacementGroupTagList(pg.Name, []machinev1beta1.TagSpecification{}, infra)

	// No placement group with that name exist, create one.
	createPlacementGroupInput := &ec2.CreatePlacementGroupInput{
		GroupName: aws.String(pg.Name),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String(ec2.ResourceTypePlacementGroup),
				Tags:         tagList,
			},
		},
	}

	switch pg.Spec.ManagementSpec.Managed.GroupType {
	case machinev1.AWSSpreadPlacementGroupType:
		createPlacementGroupInput.SetStrategy(ec2.PlacementStrategySpread)
	case machinev1.AWSClusterPlacementGroupType:
		createPlacementGroupInput.SetStrategy(ec2.PlacementStrategyCluster)
	case machinev1.AWSPartitionPlacementGroupType:
		createPlacementGroupInput.SetStrategy(ec2.PlacementStrategyPartition)

		if pg.Spec.ManagementSpec.Managed.Partition != nil && pg.Spec.ManagementSpec.Managed.Partition.Count != 0 {
			createPlacementGroupInput.SetPartitionCount(int64(pg.Spec.ManagementSpec.Managed.Partition.Count))
		}
	default:
		return fmt.Errorf("unknown aws placement strategy %q: valid values are %s, %s, %s",
			pg.Spec.ManagementSpec.Managed.GroupType,
			machinev1.AWSSpreadPlacementGroupType,
			machinev1.AWSClusterPlacementGroupType,
			machinev1.AWSPartitionPlacementGroupType)
	}

	out, err := client.CreatePlacementGroup(createPlacementGroupInput)
	if err != nil {
		// If there are any issues in creating the placement group,
		// the Ready condition will turn false and detail the error that occurred.
		werr := fmt.Errorf("failed to create aws placement group: %w", err)

		meta.SetStatusCondition(&pg.Status.Conditions, metav1.Condition{
			Type:    readyConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  creationFailedConditionReason,
			Message: werr.Error(),
		})

		return werr
	}

	// Set successful condition for placement group creation.
	condition := metav1.Condition{
		Type:   readyConditionType,
		Status: metav1.ConditionTrue,
		Reason: creationSucceededConditionReason,
	}
	meta.SetStatusCondition(&pg.Status.Conditions, condition)

	logger.Info(fmt.Sprintf("successfully created aws placement group with name: %s, id: %s",
		*out.PlacementGroup.GroupName, *out.PlacementGroup.GroupId))

	return nil
}

// validateExistingPlacementGroupConfig validates that the configuration of the existing placement group
// matches the configuration of the AWSPlacementGroup spec.
func validateExistingPlacementGroupConfig(pg *machinev1.AWSPlacementGroup, placementGroup *ec2.PlacementGroup) error {
	if placementGroup == nil {
		return fmt.Errorf("found nil aws placement group")
	}

	if aws.StringValue(placementGroup.GroupName) != pg.Name {
		return fmt.Errorf("name mismatch between configured and existing values: wanted: %q, got: %q",
			pg.Name, aws.StringValue(placementGroup.GroupName))
	}

	var expectedPlacementGroupType string

	switch pg.Spec.ManagementSpec.Managed.GroupType {
	case machinev1.AWSSpreadPlacementGroupType:
		expectedPlacementGroupType = ec2.PlacementStrategySpread
	case machinev1.AWSClusterPlacementGroupType:
		expectedPlacementGroupType = ec2.PlacementStrategyCluster
	case machinev1.AWSPartitionPlacementGroupType:
		expectedPlacementGroupType = ec2.PlacementStrategyPartition
	default:
		return fmt.Errorf("unknown placement strategy %q: valid values are %s, %s and %s",
			pg.Spec.ManagementSpec.Managed.GroupType, machinev1.AWSSpreadPlacementGroupType,
			machinev1.AWSClusterPlacementGroupType, machinev1.AWSPartitionPlacementGroupType)
	}

	if aws.StringValue(placementGroup.Strategy) != expectedPlacementGroupType {
		return fmt.Errorf("type mismatch between configured and existing values: wanted: %q, got: %q",
			expectedPlacementGroupType, aws.StringValue(placementGroup.Strategy))
	}

	if pg.Spec.ManagementSpec.Managed.GroupType == machinev1.AWSPartitionPlacementGroupType &&
		pg.Spec.ManagementSpec.Managed.Partition != nil {
		if pg.Spec.ManagementSpec.Managed.Partition.Count != 0 &&
			int64(pg.Spec.ManagementSpec.Managed.Partition.Count) != aws.Int64Value(placementGroup.PartitionCount) {
			return fmt.Errorf("group partition count mismatch between configured and existing values: wanted: %d, got: %d",
				pg.Spec.ManagementSpec.Managed.Partition.Count, aws.Int64Value(placementGroup.PartitionCount))
		}
	}

	return nil
}

// deletePlacementGroup deletes the placement group for the machine.
func deletePlacementGroup(client awsclient.Client, logger logr.Logger, pg *machinev1.AWSPlacementGroup, infra *configv1.Infrastructure) error {
	placementGroups, err := client.DescribePlacementGroups(&ec2.DescribePlacementGroupsInput{
		GroupNames: []*string{aws.String(pg.Name)},
	})

	if err != nil && !isAWS4xxError(err) {
		// Ignore a 400 error as AWS will report an unknown placement group as a 400.
		return fmt.Errorf("could not describe aws placement groups: %w", err)
	}

	switch {
	case len(placementGroups.PlacementGroups) > 1:
		return fmt.Errorf("expected 1 aws placement group for name %q, got %d", pg.Name, len(placementGroups.PlacementGroups))
	case len(placementGroups.PlacementGroups) == 0:
		// This is the normal path, the named placement group doesn't exist.
		return nil
	}

	placementGroup := placementGroups.PlacementGroups[0]
	clusterID := infra.Status.InfrastructureName

	found := false
	// Check that the placement group has a cluster tag.
	for _, tag := range placementGroup.Tags {
		if aws.StringValue(tag.Key) == "kubernetes.io/cluster/"+clusterID && aws.StringValue(tag.Value) == "owned" {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("aws placement group was not created by machine-api")
	}

	// Check that the placement group contains no instances.
	result, err := client.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{Name: aws.String("placement-group-name"), Values: []*string{aws.String(pg.Name)}},
		},
	})
	if err != nil {
		return fmt.Errorf("could not get the number of instances in aws placement group: %w", err)
	}

	var instanceCount int

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			// Ignore the Terminated instances,
			// these should not count towards instances actively in a placement group.
			if aws.StringValue(instance.State.Name) != "terminated" {
				instanceCount++
			}
		}
	}

	if instanceCount > 0 {
		return fmt.Errorf("aws placement group still contains %d instances", instanceCount)
	}

	// Only one placement group with the given name exists and it is empty, so we remove it.
	deletePlacementGroupInput := &ec2.DeletePlacementGroupInput{GroupName: aws.String(pg.Name)}
	if _, err := client.DeletePlacementGroup(deletePlacementGroupInput); err != nil {
		return fmt.Errorf("could not remove the cloud resource on aws: %w", err)
	}

	logger.Info("successfully deleted aws placement group")

	return nil
}

// reflectObservedConfiguration checks for the existence of a placement group on AWS and if that's the case
// it syncs its config with the ObservedConfiguration in the Status of the object.
func reflectObservedConfiguration(client awsclient.Client, logger logr.Logger, pg *machinev1.AWSPlacementGroup) error {
	placementGroups, err := client.DescribePlacementGroups(&ec2.DescribePlacementGroupsInput{
		GroupNames: []*string{aws.String(pg.Name)},
	})
	if err != nil && !isAWS4xxError(err) {
		// Ignore a 400 error as AWS will report an unknown placement group as a 400.
		return fmt.Errorf("could not describe aws placement groups: %w", err)
	}

	switch {
	case len(placementGroups.PlacementGroups) > 1:
		// Only one placement group was expected to match this name.
		return fmt.Errorf("expected 1 aws placement group for name %s, got %d", pg.Name, len(placementGroups.PlacementGroups))
	case len(placementGroups.PlacementGroups) < 1:
		// No placement groups are present with this name at this time yet.
		logger.Info(fmt.Sprintf("no matching aws placement group for name %s", pg.Name))
	default:
		// Exactly 1 placement group exists with the this name,
		// observe its configuration and set it on the object Status.
		logger.Info(fmt.Sprintf("found 1 aws placement group for name %s with id %s", pg.Name, *placementGroups.PlacementGroups[0].GroupId))
		setObservedConfiguration(pg, placementGroups.PlacementGroups[0])
	}

	// Set the .Status.ExpiresAt in 2 minutes from now, to keep a TTL cache
	// of the configuration observed from AWS.
	inTwoMinutes := metav1.NewTime(metav1.Now().Add(2 * time.Minute))
	pg.Status.ExpiresAt = &inTwoMinutes

	return nil
}

// setObservedConfiguration sets the configuration observed from the AWS placement group to
// the ObservedConfiguration field in the Status of the object.
func setObservedConfiguration(pg *machinev1.AWSPlacementGroup, placementGroup *ec2.PlacementGroup) {
	pg.Status.ObservedConfiguration.GroupType = machinev1.AWSPlacementGroupType(strings.Title(aws.StringValue(placementGroup.Strategy)))
	pg.Status.ObservedConfiguration.Partition = &machinev1.AWSPartitionPlacement{Count: int32(aws.Int64Value(placementGroup.PartitionCount))}

	condition := metav1.Condition{Type: readyConditionType}

	var equal bool
	if pg.Spec.ManagementSpec.Managed != nil {
		equal = reflect.DeepEqual(pg.Status.ObservedConfiguration, *pg.Spec.ManagementSpec.Managed)
	}

	if equal {
		condition.Status = metav1.ConditionTrue
		condition.Reason = configurationInSyncConditionReason
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = configurationMismatchConditionReason
	}

	meta.SetStatusCondition(&pg.Status.Conditions, condition)
}

// validateAWSPlacementGroup validates an AWSPlacementGroup configuration.
func validateAWSPlacementGroup(pg *machinev1.AWSPlacementGroup) error {
	// First validation should happen via webhook before the object is persisted.
	// This is a complementary validation to fail early in case of lacking proper webhook validation.
	switch pg.Spec.ManagementSpec.ManagementState {
	case machinev1.ManagedManagementState, machinev1.UnmanagedManagementState:
		// valid values
	default:
		return fmt.Errorf("invalid aws placement group. spec.managementSpec.managementState must either be %s or %s",
			machinev1.ManagedManagementState, machinev1.UnmanagedManagementState)
	}

	if pg.Spec.ManagementSpec.ManagementState == machinev1.ManagedManagementState {
		if pg.Spec.ManagementSpec.Managed == nil {
			return fmt.Errorf("invalid aws placement group. spec.managementSpec.managed must not be nil when spec.managementSpec.managementState is %s",
				machinev1.ManagedManagementState)
		}

		// A Managed placement group may be moved to Unmanaged, however an Unmanaged
		// group may not be moved back to Managed.
		if pg.Status.ManagementState == machinev1.UnmanagedManagementState {
			return fmt.Errorf("invalid aws placement group. spec.managementSpec.managementState cannot be set to %s once it has been set to %s",
				machinev1.ManagedManagementState, machinev1.UnmanagedManagementState)
		}
	}

	if pg.Spec.ManagementSpec.Managed != nil {
		switch pg.Spec.ManagementSpec.Managed.GroupType {
		case machinev1.AWSClusterPlacementGroupType, machinev1.AWSPartitionPlacementGroupType, machinev1.AWSSpreadPlacementGroupType:
			// valid values
		default:
			return fmt.Errorf("invalid aws placement group. spec.managementSpec.managed.groupType must either be %s, %s or %s",
				machinev1.AWSClusterPlacementGroupType, machinev1.AWSPartitionPlacementGroupType, machinev1.AWSSpreadPlacementGroupType)
		}

		if pg.Spec.ManagementSpec.Managed.Partition != nil {
			if pg.Spec.ManagementSpec.Managed.Partition.Count < 1 || pg.Spec.ManagementSpec.Managed.Partition.Count > 7 {
				return fmt.Errorf("invalid aws placement group. spec.managementSpec.managed.partition.count must be greater" +
					" or equal than 1 and less or equal than 7")
			}
		}
	}

	return nil
}

// requeueAt returns the time.Duration that represents the amount
// of time before to wait before requeuing.
// If the computed time.Duration is negative,
// meaning the provided time.Time is in the past, it returns 0s.
func requeueAt(at time.Time) time.Duration {
	duration := -time.Since(at.Add(1 * time.Millisecond))
	if duration < 0 {
		return 0
	}

	return duration
}
