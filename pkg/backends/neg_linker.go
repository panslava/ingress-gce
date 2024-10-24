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

package backends

import (
	"fmt"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/cloud-provider-gcp/providers/gce"
	negv1beta1 "k8s.io/ingress-gce/pkg/apis/svcneg/v1beta1"
	befeatures "k8s.io/ingress-gce/pkg/backends/features"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/ingress-gce/pkg/neg/types"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
)

// negLinker handles linking backends to NEG's.
type negLinker struct {
	backendPool  Pool
	negGetter    NEGGetter
	cloud        *gce.Cloud
	svcNegLister cache.Indexer

	logger klog.Logger
}

// negLinker is a Linker
var _ Linker = (*negLinker)(nil)

func NewNEGLinker(
	backendPool Pool,
	negGetter NEGGetter,
	cloud *gce.Cloud,
	svcNegLister cache.Indexer,
	logger klog.Logger,
) Linker {
	return &negLinker{
		backendPool:  backendPool,
		negGetter:    negGetter,
		cloud:        cloud,
		svcNegLister: svcNegLister,
		logger:       logger.WithName("NEGLinker"),
	}
}

// Link implements Link.
func (nl *negLinker) Link(sp utils.ServicePort, groups []GroupKey) error {
	version := befeatures.VersionFromServicePort(&sp)

	negSelfLinks, err := nl.getNegSelfLinks(sp, groups)
	if err != nil {
		return err
	}

	beName := sp.BackendName()
	scope := befeatures.ScopeFromServicePort(&sp)

	key, err := composite.CreateKey(nl.cloud, beName, scope)
	if err != nil {
		return err
	}
	backendService, err := composite.GetBackendService(nl.cloud, key, version, nl.logger)
	if err != nil {
		return err
	}

	newBackends := backendsForNEGs(negSelfLinks, &sp)
	// merge backends
	mergedBackend, err := mergeBackends(backendService.Backends, newBackends)
	if err != nil {
		nl.logger.Error(err, fmt.Sprintf("Failed to merge backends from %#v and %#v", backendService.Backends, newBackends))
		nl.logger.Info("Fall back to ensure backend service with newBackends.")
		mergedBackend = newBackends
	}

	diff := diffBackends(backendService.Backends, mergedBackend, nl.logger)
	if diff.isEqual() {
		nl.logger.V(2).Info("No changes in backends for service port", "servicePort", sp.ID)
		return nil
	}
	nl.logger.V(2).Info("Backends changed for service port", "servicePort", sp.ID, "removing", diff.toRemove(), "adding", diff.toAdd(), "changed", diff.changed)

	backendService.Backends = mergedBackend
	return composite.UpdateBackendService(nl.cloud, key, backendService, nl.logger)
}

func (nl *negLinker) getNegSelfLinks(sp utils.ServicePort, groups []GroupKey) ([]string, error) {
	version := befeatures.VersionFromServicePort(&sp)
	negName := sp.NEGName()
	svcNegKey := fmt.Sprintf("%s/%s", sp.ID.Service.Namespace, negName)
	urls, ok := getNegUrlsFromSvcneg(svcNegKey, nl.svcNegLister, nl.logger)
	if ok {
		return urls, nil
	}

	var negSelfLinks []string
	// In fail-safe situation, we only link NEGs in the default subnet.
	// We will add all NEGs once CRD is available.
	for _, group := range groups {
		nl.logger.V(4).Info("Falling back to use NEG API to retrieve NEG url for NEG", "negName", negName)
		neg, err := nl.negGetter.GetNetworkEndpointGroup(negName, group.Zone, version, nl.logger)
		if err != nil {
			return nil, err
		}
		negSelfLinks = append(negSelfLinks, neg.SelfLink)
	}
	return negSelfLinks, nil
}

type backendDiff struct {
	old     sets.String
	new     sets.String
	changed sets.String
}

// getNegMergeGroupKey takes the resource url of  NEG and return the merge key to uniquely identify a NEG in $zone/$NEG format
func getNegMergeGroupKey(negUrl string) (meta.Key, error) {
	id, err := cloud.ParseResourceURL(negUrl)
	if err != nil {
		return meta.Key{}, err
	}
	return *id.Key, nil
}

// mergeBackends merges the both input list of backends into one
func mergeBackends(old, new []*composite.Backend) ([]*composite.Backend, error) {
	backendMap := map[meta.Key]*composite.Backend{}
	for _, be := range new {
		key, err := getNegMergeGroupKey(be.Group)
		if err != nil {
			return []*composite.Backend{}, err
		}
		backendMap[key] = be
	}

	for _, be := range old {
		key, err := getNegMergeGroupKey(be.Group)
		if err != nil {
			return []*composite.Backend{}, err
		}
		if _, ok := backendMap[key]; !ok {
			backendMap[key] = be
		}
	}

	ret := []*composite.Backend{}

	for _, be := range backendMap {
		ret = append(ret, be)
	}
	return ret, nil
}

func diffBackends(old, new []*composite.Backend, logger klog.Logger) *backendDiff {
	d := &backendDiff{
		old:     sets.NewString(),
		new:     sets.NewString(),
		changed: sets.NewString(),
	}

	oldMap := map[string]*composite.Backend{}
	for _, be := range old {
		beGroup := relativeResourceNameWithDefault(be.Group, logger)
		d.old.Insert(beGroup)
		oldMap[beGroup] = be
	}
	for _, be := range new {
		beGroup := relativeResourceNameWithDefault(be.Group, logger)
		d.new.Insert(beGroup)

		if oldBe, ok := oldMap[beGroup]; ok {
			// Note: if you are comparing a value that has a non-zero default
			// value (e.g. CapacityScaler is 1.0), you will need to set that
			// value when creating a new Backend to avoid a false positive when
			// computing diffs.
			if flags.F.EnableTrafficScaling {
				var changed bool
				changed = changed || oldBe.MaxRatePerEndpoint != be.MaxRatePerEndpoint
				changed = changed || oldBe.CapacityScaler != be.CapacityScaler
				if changed {
					d.changed.Insert(beGroup)
				}
			}
		}
	}

	return d
}

func (d *backendDiff) isEqual() bool         { return d.old.Equal(d.new) && d.changed.Len() == 0 }
func (d *backendDiff) toRemove() sets.String { return d.old.Difference(d.new) }
func (d *backendDiff) toAdd() sets.String    { return d.new.Difference(d.old) }

func backendsForNEGs(negSelfLinks []string, sp *utils.ServicePort) []*composite.Backend {
	var backends []*composite.Backend
	for _, neg := range negSelfLinks {
		newBackend := &composite.Backend{Group: neg}

		switch getNegType(*sp) {
		case types.VmIpEndpointType:
			// Setting MaxConnectionsPerEndpoint is not supported for L4 ILB
			// https://cloud.google.com/load-balancing/docs/backend-service#target_capacity
			// hence only mode is being set.
			newBackend.BalancingMode = string(Connections)

		case types.VmIpPortEndpointType:
			// This preserves the original behavior, but really we should error
			// when there is a type we don't understand.
			fallthrough
		default:
			newBackend.BalancingMode = string(Rate)
			newBackend.MaxRatePerEndpoint = maxRPS
			newBackend.CapacityScaler = 1.0

			if flags.F.EnableTrafficScaling {
				if sp.MaxRatePerEndpoint != nil {
					newBackend.MaxRatePerEndpoint = float64(*sp.MaxRatePerEndpoint)
				}
				if sp.CapacityScaler != nil {
					newBackend.CapacityScaler = *sp.CapacityScaler
				}
			}
		}

		backends = append(backends, newBackend)
	}
	return backends
}

// getNegType returns NEG type based on service port config
func getNegType(sp utils.ServicePort) types.NetworkEndpointType {
	if sp.VMIPNEGEnabled {
		return types.VmIpEndpointType
	}
	return types.VmIpPortEndpointType
}

// getNegUrlsFromSvcneg return NEG urls from svcneg status
func getNegUrlsFromSvcneg(key string, svcNegLister cache.Indexer, logger klog.Logger) ([]string, bool) {
	var negUrls []string
	obj, exists, err := svcNegLister.GetByKey(key)
	if err != nil {
		logger.Error(err, "Failed to retrieve svcneg from cache", "svcneg", key)
		return nil, false
	}
	if !exists {
		return nil, false
	}
	svcneg := obj.(*negv1beta1.ServiceNetworkEndpointGroup)

	for _, negRef := range svcneg.Status.NetworkEndpointGroups {
		negUrls = append(negUrls, negRef.SelfLink)
	}
	return negUrls, true
}

// relativeResourceNameWithDefault will attempt to return a RelativeResourceName
// for the provided `selfLink`. In case of a faiure, it will return the
// `selfLink` itself.
func relativeResourceNameWithDefault(selfLink string, logger klog.Logger) string {
	result, err := utils.RelativeResourceName(selfLink)
	if err != nil {
		logger.Info("Unable to parse resource name from selfLink", "selfLink", selfLink)
		return selfLink
	}
	return result
}
