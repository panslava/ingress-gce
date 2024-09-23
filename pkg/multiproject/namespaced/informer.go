package namespaced

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/cache"
)

// Informer wraps a SharedIndexInformer to provide a namespaced view.
type Informer struct {
	cache.SharedIndexInformer
	namespace string
}

// NewInformer creates a new Informer.
func NewInformer(informer cache.SharedIndexInformer, namespace string) cache.SharedIndexInformer {
	return &Informer{
		SharedIndexInformer: informer,
		namespace:           namespace,
	}
}

// AddEventHandler adds an event handler that only processes events for the specified namespace.
func (i *Informer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return i.SharedIndexInformer.AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: i.namespaceFilter,
			Handler:    handler,
		},
	)
}

// AddEventHandlerWithResyncPeriod adds an event handler with resync period.
func (i *Informer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return i.SharedIndexInformer.AddEventHandlerWithResyncPeriod(
		cache.FilteringResourceEventHandler{
			FilterFunc: i.namespaceFilter,
			Handler:    handler,
		},
		resyncPeriod,
	)
}

// namespaceFilter filters objects based on the namespace.
func (i *Informer) namespaceFilter(obj interface{}) bool {
	metaObj, err := meta.Accessor(obj)
	if err != nil {
		return false
	}
	return metaObj.GetNamespace() == i.namespace
}

func (i *Informer) GetStore() cache.Store {
	return &store{
		Store:     i.SharedIndexInformer.GetStore(),
		namespace: i.namespace,
	}
}

func (i *Informer) GetIndexer() cache.Indexer {
	return &indexer{
		Indexer:   i.SharedIndexInformer.GetIndexer(),
		namespace: i.namespace,
	}
}
