package namespaced

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/cache"
)

// indexer wraps an Indexer to provide namespace filtering.
type indexer struct {
	cache.Indexer
	namespace string
}

func (n *indexer) List() []interface{} {
	return n.namespaceFilteredList(n.Indexer.List())
}

func (n *indexer) ByIndex(indexName, indexKey string) ([]interface{}, error) {
	items, err := n.Indexer.ByIndex(indexName, indexKey)
	if err != nil {
		return nil, err
	}
	return n.namespaceFilteredList(items), nil
}

func (n *indexer) Index(indexName string, obj interface{}) ([]interface{}, error) {
	items, err := n.Indexer.Index(indexName, obj)
	if err != nil {
		return nil, err
	}
	return n.namespaceFilteredList(items), nil
}

func (n *indexer) namespaceFilteredList(items []interface{}) []interface{} {
	var filtered []interface{}
	for _, item := range items {
		if metaObj, err := meta.Accessor(item); err == nil {
			if metaObj.GetNamespace() == n.namespace {
				filtered = append(filtered, item)
			}
		}
	}
	return filtered
}
