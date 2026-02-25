package e2e

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	ginkgov2 "github.com/onsi/ginkgo/v2"
)

// newClientConfigForTest returns a config configured to connect to the api server
func newClientConfigForTest() (*rest.Config, error) {
	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, &clientcmd.ConfigOverrides{ClusterInfo: api.Cluster{InsecureSkipTLSVerify: true}})
	config, err := clientConfig.ClientConfig()
	if err == nil {
		ginkgov2.GinkgoLogr.Info("Found configuration for", "host", config.Host)
	}
	return config, err
}
