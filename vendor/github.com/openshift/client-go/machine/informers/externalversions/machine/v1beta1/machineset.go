// Code generated by informer-gen. DO NOT EDIT.

package v1beta1

import (
	context "context"
	time "time"

	apimachinev1beta1 "github.com/openshift/api/machine/v1beta1"
	versioned "github.com/openshift/client-go/machine/clientset/versioned"
	internalinterfaces "github.com/openshift/client-go/machine/informers/externalversions/internalinterfaces"
	machinev1beta1 "github.com/openshift/client-go/machine/listers/machine/v1beta1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// MachineSetInformer provides access to a shared informer and lister for
// MachineSets.
type MachineSetInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() machinev1beta1.MachineSetLister
}

type machineSetInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewMachineSetInformer constructs a new informer for MachineSet type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewMachineSetInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredMachineSetInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredMachineSetInformer constructs a new informer for MachineSet type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredMachineSetInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineV1beta1().MachineSets(namespace).List(context.Background(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineV1beta1().MachineSets(namespace).Watch(context.Background(), options)
			},
			ListWithContextFunc: func(ctx context.Context, options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineV1beta1().MachineSets(namespace).List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineV1beta1().MachineSets(namespace).Watch(ctx, options)
			},
		},
		&apimachinev1beta1.MachineSet{},
		resyncPeriod,
		indexers,
	)
}

func (f *machineSetInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredMachineSetInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *machineSetInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apimachinev1beta1.MachineSet{}, f.defaultInformer)
}

func (f *machineSetInformer) Lister() machinev1beta1.MachineSetLister {
	return machinev1beta1.NewMachineSetLister(f.Informer().GetIndexer())
}
