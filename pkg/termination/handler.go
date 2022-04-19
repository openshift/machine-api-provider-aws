package termination

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	awsrequest "github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	awsTerminationEndpointURL                           = "/latest/meta-data/spot/termination-time"
	terminatingConditionType   corev1.NodeConditionType = "Terminating"
	terminationRequestedReason                          = "TerminationRequested"
)

// Handler represents a handler that will run to check the termination notice
// endpoint and mark node for deletion if the instance termination notice is fulfilled.
type Handler interface {
	Run(stop <-chan struct{}) error
}

// NewHandler constructs a new Handler
func NewHandler(logger logr.Logger, cfg *rest.Config, pollInterval time.Duration, namespace, nodeName string) (Handler, error) {
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, fmt.Errorf("error creating client: %v", err)
	}

	logger = logger.WithValues("node", nodeName, "namespace", namespace)

	return &handler{
		client:       c,
		pollInterval: pollInterval,
		nodeName:     nodeName,
		namespace:    namespace,
		log:          logger,
	}, nil
}

// handler implements the logic to check the termination endpoint and
// marks the node for termination
type handler struct {
	client client.Client
	// endpoint - custom imds service url. For testing purposes.
	endpoint     *string
	pollInterval time.Duration
	nodeName     string
	namespace    string
	log          logr.Logger
}

// Run starts the handler and runs the termination logic
func (h *handler) Run(stop <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())

	imdsSession := session.Must(session.NewSession(&aws.Config{
		MaxRetries: aws.Int(3),
		Endpoint:   h.endpoint,
	}))
	imdsClient := ec2metadata.New(imdsSession)

	errs := make(chan error, 1)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		errs <- h.run(ctx, imdsClient, wg)
	}()

	select {
	case <-stop:
		cancel()
		// Wait for run to stop
		wg.Wait()
		return nil
	case err := <-errs:
		cancel()
		return err
	}
}

func (h *handler) run(ctx context.Context, imdsClient *ec2metadata.EC2Metadata, wg *sync.WaitGroup) error {
	defer wg.Done()

	logger := h.log.WithValues("node", h.nodeName)
	logger.V(1).Info("Monitoring node termination")

	if err := wait.PollImmediateUntil(h.pollInterval, func() (bool, error) {
		// code below mostly replicates GetMetadataWithContext method of the imdsClient.
		// https://github.com/aws/aws-sdk-go/blob/v1.43.20/aws/ec2metadata/api.go#L61
		// Since it's not possible to reliably extract information from result of such function, manual request prep
		// and handling happens here.
		op := &awsrequest.Operation{
			Name:       "GetMetadata",
			HTTPMethod: "GET",
			HTTPPath:   awsTerminationEndpointURL,
		}
		req := imdsClient.NewRequest(op, nil, nil)
		req.SetContext(ctx)
		// we do not care about response data, all what we are interesting about is the status code.
		// successful request means that instance was marked for termination.
		// If instance not yet marked, response with 404 code will be returned from imds
		err := req.Send()
		if err != nil {
			if req.HTTPResponse.StatusCode == http.StatusNotFound {
				logger.V(2).Info("Instance not marked for termination")
				return false, nil
			}
			return false, fmt.Errorf("%w", err)
		}
		// successful request, instance marked for termination. Done here.
		return true, nil
	}, ctx.Done()); err != nil {
		return fmt.Errorf("error polling termination endpoint: %v", err)
	}

	// Will only get here if the termination endpoint returned 200
	logger.V(1).Info("Instance marked for termination, marking Node for deletion")
	if err := h.markNodeForDeletion(ctx); err != nil {
		return fmt.Errorf("error marking node: %v", err)
	}

	return nil
}

func (h *handler) markNodeForDeletion(ctx context.Context) error {
	node := &corev1.Node{}
	if err := h.client.Get(ctx, client.ObjectKey{Name: h.nodeName}, node); err != nil {
		return fmt.Errorf("error fetching node: %v", err)
	}

	addNodeTerminationCondition(node)
	if err := h.client.Status().Update(ctx, node); err != nil {
		return fmt.Errorf("error updating node status")
	}
	return nil
}

// nodeHasTerminationCondition checks whether the node already
// has a condition with the terminatingConditionType type
func nodeHasTerminationCondition(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == terminatingConditionType {
			return true
		}
	}
	return false
}

// addNodeTerminationCondition will add a condition with a
// terminatingConditionType type to the node
func addNodeTerminationCondition(node *corev1.Node) {
	now := metav1.Now()
	terminatingCondition := corev1.NodeCondition{
		Type:               terminatingConditionType,
		Status:             corev1.ConditionTrue,
		LastHeartbeatTime:  now,
		LastTransitionTime: now,
		Reason:             terminationRequestedReason,
		Message:            "The cloud provider has marked this instance for termination",
	}

	if !nodeHasTerminationCondition(node) {
		// No need to merge, just add the new condition to the end
		node.Status.Conditions = append(node.Status.Conditions, terminatingCondition)
		return
	}

	// The node already has a terminating condition,
	// so make sure it has the correct status
	conditions := []corev1.NodeCondition{}
	for _, condition := range node.Status.Conditions {
		if condition.Type != terminatingConditionType {
			conditions = append(conditions, condition)
			continue
		}

		// Condition type is terminating
		if condition.Status == corev1.ConditionTrue {
			// Condition already marked true, do not update
			conditions = append(conditions, condition)
			continue
		}

		// The existing terminating condition had the wrong status
		conditions = append(conditions, terminatingCondition)
	}

	node.Status.Conditions = conditions
}
