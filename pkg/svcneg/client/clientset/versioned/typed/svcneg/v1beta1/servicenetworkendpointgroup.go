/*
Copyright 2023 The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1beta1

import (
	"context"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
	v1beta1 "k8s.io/ingress-gce/pkg/apis/svcneg/v1beta1"
	scheme "k8s.io/ingress-gce/pkg/svcneg/client/clientset/versioned/scheme"
)

// ServiceNetworkEndpointGroupsGetter has a method to return a ServiceNetworkEndpointGroupInterface.
// A group's client should implement this interface.
type ServiceNetworkEndpointGroupsGetter interface {
	ServiceNetworkEndpointGroups(namespace string) ServiceNetworkEndpointGroupInterface
}

// ServiceNetworkEndpointGroupInterface has methods to work with ServiceNetworkEndpointGroup resources.
type ServiceNetworkEndpointGroupInterface interface {
	Create(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.CreateOptions) (*v1beta1.ServiceNetworkEndpointGroup, error)
	Update(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.UpdateOptions) (*v1beta1.ServiceNetworkEndpointGroup, error)
	UpdateStatus(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.UpdateOptions) (*v1beta1.ServiceNetworkEndpointGroup, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1beta1.ServiceNetworkEndpointGroup, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1beta1.ServiceNetworkEndpointGroupList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.ServiceNetworkEndpointGroup, err error)
	ServiceNetworkEndpointGroupExpansion
}

// serviceNetworkEndpointGroups implements ServiceNetworkEndpointGroupInterface
type serviceNetworkEndpointGroups struct {
	client rest.Interface
	ns     string
}

// newServiceNetworkEndpointGroups returns a ServiceNetworkEndpointGroups
func newServiceNetworkEndpointGroups(c *NetworkingV1beta1Client, namespace string) *serviceNetworkEndpointGroups {
	return &serviceNetworkEndpointGroups{
		client: c.RESTClient(),
		ns:     namespace,
	}
}

// Get takes name of the serviceNetworkEndpointGroup, and returns the corresponding serviceNetworkEndpointGroup object, and an error if there is any.
func (c *serviceNetworkEndpointGroups) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1beta1.ServiceNetworkEndpointGroup, err error) {
	result = &v1beta1.ServiceNetworkEndpointGroup{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of ServiceNetworkEndpointGroups that match those selectors.
func (c *serviceNetworkEndpointGroups) List(ctx context.Context, opts v1.ListOptions) (result *v1beta1.ServiceNetworkEndpointGroupList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1beta1.ServiceNetworkEndpointGroupList{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested serviceNetworkEndpointGroups.
func (c *serviceNetworkEndpointGroups) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a serviceNetworkEndpointGroup and creates it.  Returns the server's representation of the serviceNetworkEndpointGroup, and an error, if there is any.
func (c *serviceNetworkEndpointGroups) Create(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.CreateOptions) (result *v1beta1.ServiceNetworkEndpointGroup, err error) {
	result = &v1beta1.ServiceNetworkEndpointGroup{}
	err = c.client.Post().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(serviceNetworkEndpointGroup).
		Do(ctx).
		Into(result)
	return
}

// Update takes the representation of a serviceNetworkEndpointGroup and updates it. Returns the server's representation of the serviceNetworkEndpointGroup, and an error, if there is any.
func (c *serviceNetworkEndpointGroups) Update(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.UpdateOptions) (result *v1beta1.ServiceNetworkEndpointGroup, err error) {
	result = &v1beta1.ServiceNetworkEndpointGroup{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		Name(serviceNetworkEndpointGroup.Name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(serviceNetworkEndpointGroup).
		Do(ctx).
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *serviceNetworkEndpointGroups) UpdateStatus(ctx context.Context, serviceNetworkEndpointGroup *v1beta1.ServiceNetworkEndpointGroup, opts v1.UpdateOptions) (result *v1beta1.ServiceNetworkEndpointGroup, err error) {
	result = &v1beta1.ServiceNetworkEndpointGroup{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		Name(serviceNetworkEndpointGroup.Name).
		SubResource("status").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(serviceNetworkEndpointGroup).
		Do(ctx).
		Into(result)
	return
}

// Delete takes name of the serviceNetworkEndpointGroup and deletes it. Returns an error if one occurs.
func (c *serviceNetworkEndpointGroups) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *serviceNetworkEndpointGroups) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		VersionedParams(&listOpts, scheme.ParameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched serviceNetworkEndpointGroup.
func (c *serviceNetworkEndpointGroups) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.ServiceNetworkEndpointGroup, err error) {
	result = &v1beta1.ServiceNetworkEndpointGroup{}
	err = c.client.Patch(pt).
		Namespace(c.ns).
		Resource("servicenetworkendpointgroups").
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}
