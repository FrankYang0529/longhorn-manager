/*
Copyright The Kubernetes Authors.

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

// Code generated by lister-gen. DO NOT EDIT.

package v1beta2

import (
	v1beta2 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// SnapshotLister helps list Snapshots.
type SnapshotLister interface {
	// List lists all Snapshots in the indexer.
	List(selector labels.Selector) (ret []*v1beta2.Snapshot, err error)
	// Snapshots returns an object that can list and get Snapshots.
	Snapshots(namespace string) SnapshotNamespaceLister
	SnapshotListerExpansion
}

// snapshotLister implements the SnapshotLister interface.
type snapshotLister struct {
	indexer cache.Indexer
}

// NewSnapshotLister returns a new SnapshotLister.
func NewSnapshotLister(indexer cache.Indexer) SnapshotLister {
	return &snapshotLister{indexer: indexer}
}

// List lists all Snapshots in the indexer.
func (s *snapshotLister) List(selector labels.Selector) (ret []*v1beta2.Snapshot, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1beta2.Snapshot))
	})
	return ret, err
}

// Snapshots returns an object that can list and get Snapshots.
func (s *snapshotLister) Snapshots(namespace string) SnapshotNamespaceLister {
	return snapshotNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// SnapshotNamespaceLister helps list and get Snapshots.
type SnapshotNamespaceLister interface {
	// List lists all Snapshots in the indexer for a given namespace.
	List(selector labels.Selector) (ret []*v1beta2.Snapshot, err error)
	// Get retrieves the Snapshot from the indexer for a given namespace and name.
	Get(name string) (*v1beta2.Snapshot, error)
	SnapshotNamespaceListerExpansion
}

// snapshotNamespaceLister implements the SnapshotNamespaceLister
// interface.
type snapshotNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all Snapshots in the indexer for a given namespace.
func (s snapshotNamespaceLister) List(selector labels.Selector) (ret []*v1beta2.Snapshot, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1beta2.Snapshot))
	})
	return ret, err
}

// Get retrieves the Snapshot from the indexer for a given namespace and name.
func (s snapshotNamespaceLister) Get(name string) (*v1beta2.Snapshot, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1beta2.Resource("snapshot"), name)
	}
	return obj.(*v1beta2.Snapshot), nil
}