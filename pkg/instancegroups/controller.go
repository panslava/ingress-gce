package instancegroups

import (
	"fmt"
	"net/http"

	"google.golang.org/api/compute/v1"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/klog/v2"
)

type Controller struct {
	cloud      CloudProvider
	zoneLister ZoneLister
	namer      namer.BackendNamer
}

// ControllerConfig is used for Controller constructor.
type ControllerConfig struct {
	Cloud      CloudProvider
	Namer      namer.BackendNamer
	ZoneLister ZoneLister
}

// NewController creates a new Instance Group Controller using ControllerConfig.
func NewController(config *ControllerConfig) *Controller {
	return &Controller{
		cloud:      config.Cloud,
		namer:      config.Namer,
		zoneLister: config.ZoneLister,
	}
}

// EnsureInstanceGroupsAndPorts creates or gets an instance group if it doesn't exist
// and adds the given ports to it. Returns a list of one instance group per zone,
// all of which have the exact same named ports.
func (igc *Controller) EnsureInstanceGroupsAndPorts(name string, ports []int64) (igs []*compute.InstanceGroup, err error) {
	// Instance groups need to be created only in zones that have ready nodes.
	zones, err := igc.zoneLister.ListZones(utils.CandidateNodesPredicate)
	if err != nil {
		return nil, err
	}

	for _, zone := range zones {
		ig, err := igc.ensureZoneInstanceGroupAndPorts(name, zone, ports)
		if err != nil {
			return nil, err
		}

		igs = append(igs, ig)
	}
	return igs, nil
}

func (igc *Controller) ensureZoneInstanceGroupAndPorts(name, zone string, ports []int64) (*compute.InstanceGroup, error) {
	ig, err := igc.Get(name, zone)
	if err != nil && !utils.IsHTTPErrorCode(err, http.StatusNotFound) {
		klog.Errorf("Failed to get instance group %v/%v, err: %v", zone, name, err)
		return nil, err
	}

	if ig == nil {
		klog.V(3).Infof("Creating instance group %v/%v.", zone, name)
		if err = igc.cloud.CreateInstanceGroup(&compute.InstanceGroup{Name: name}, zone); err != nil {
			// Error may come back with StatusConflict meaning the instance group was created by another controller
			// possibly the Service Controller for internal load balancers.
			if utils.IsHTTPErrorCode(err, http.StatusConflict) {
				klog.Warningf("Failed to create instance group %v/%v due to conflict status, but continuing sync. err: %v", zone, name, err)
			} else {
				klog.Errorf("Failed to create instance group %v/%v, err: %v", zone, name, err)
				return nil, err
			}
		}
		ig, err = igc.cloud.GetInstanceGroup(name, zone)
		if err != nil {
			klog.Errorf("Failed to get instance group %v/%v after ensuring existence, err: %v", zone, name, err)
			return nil, err
		}
	} else {
		klog.V(5).Infof("Instance group %v/%v already exists.", zone, name)
	}

	// Build map of existing ports
	existingPorts := map[int64]bool{}
	for _, np := range ig.NamedPorts {
		existingPorts[np.Port] = true
	}

	// Determine which ports need to be added
	var newPorts []int64
	for _, p := range ports {
		if existingPorts[p] {
			klog.V(5).Infof("Instance group %v/%v already has named port %v", zone, ig.Name, p)
			continue
		}
		newPorts = append(newPorts, p)
	}

	// Build slice of NamedPorts for adding
	var newNamedPorts []*compute.NamedPort
	for _, port := range newPorts {
		newNamedPorts = append(newNamedPorts, &compute.NamedPort{Name: igc.namer.NamedPort(port), Port: port})
	}

	if len(newNamedPorts) > 0 {
		klog.V(3).Infof("Instance group %v/%v does not have ports %+v, adding them now.", zone, name, newPorts)
		if err := igc.cloud.SetNamedPortsOfInstanceGroup(ig.Name, zone, append(ig.NamedPorts, newNamedPorts...)); err != nil {
			return nil, err
		}
	}

	return ig, nil
}

// DeleteInAllZones deletes the given IG by name, from all zones.
func (igc *Controller) DeleteInAllZones(name string) error {
	var errs []error

	zones, err := igc.zoneLister.ListZones(utils.AllNodesPredicate)
	if err != nil {
		return err
	}
	for _, zone := range zones {
		if err := igc.Delete(name, zone); err != nil {
			errs = append(errs, err)
		} else {
			klog.V(3).Infof("Deleted instance group %v in zone %v", name, zone)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%v", errs)
}

func (igc *Controller) Delete(name, zone string) error {
	err := igc.cloud.DeleteInstanceGroup(name, zone)
	if err != nil {
		if utils.IsNotFoundError(err) {
			klog.V(3).Infof("Instance group %v in zone %v did not exist", name, zone)
		} else if utils.IsInUsedByError(err) {
			klog.V(3).Infof("Could not delete instance group %v in zone %v because it's still in use. Ignoring: %v", name, zone, err)
		} else {
			return err
		}
	}
	return nil
}

// Get returns the Instance Group by name and zone.
func (igc *Controller) Get(name, zone string) (*compute.InstanceGroup, error) {
	ig, err := igc.cloud.GetInstanceGroup(name, zone)
	if err != nil {
		return nil, err
	}

	return ig, nil
}
