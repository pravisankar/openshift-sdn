package osdn

import (
	log "github.com/golang/glog"

	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace/vnid"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
)

func (oc *OvsController) VnidStartMaster() error {
	go watchNamespaces(oc)
	return nil
}

func watchNamespaces(oc *OvsController) {
	nsevent := make(chan *api.NamespaceEvent)
	go oc.Registry.WatchNamespaces(nsevent)
	for {
		ev := <-nsevent
		switch ev.Type {
		case api.Added:
			_, err := oc.Registry.GetNetNamespace(ev.Name)
			if err == nil {
				continue
			}
			err = oc.Registry.CreateNetNamespace(ev.Name)
			if err != nil {
				log.Errorf("Error creating NetNamespace: %v", err)
				continue
			}
		case api.Deleted:
			err := oc.Registry.DeleteNetNamespace(ev.Name)
			if err != nil {
				log.Errorf("Error deleting NetNamespace: %v", err)
				continue
			}
		}
	}
}

func (oc *OvsController) VnidStartNode() error {
	go watchNetNamespaces(oc)
	go watchServices(oc)
	go watchPods(oc)

	return nil
}

func (oc *OvsController) updatePodNetwork(namespace string, netID, oldNetID uint) error {
	// Update OF rules for the existing/old pods in the namespace
	pods, err := oc.Registry.GetRunningPods(oc.hostName, namespace)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		err := oc.pluginHooks.UpdatePod(pod.Namespace, pod.Name, kubetypes.DockerID(pod.ContainerID))
		if err != nil {
			return err
		}
	}

	// Update OF rules for the old services in the namespace
	services, err := oc.Registry.GetServicesForNamespace(namespace)
	if err != nil {
		return err
	}
	for _, svc := range services {
		for _, port := range svc.Ports {
			oc.flowController.DelServiceOFRules(oldNetID, svc.IP, port.Protocol, port.Port)
			oc.flowController.AddServiceOFRules(netID, svc.IP, port.Protocol, port.Port)
		}
	}
	return nil
}

func watchNetNamespaces(oc *OvsController) {
	netNsEvent := make(chan *api.NetNamespaceEvent)
	go oc.Registry.WatchNetNamespaces(netNsEvent)
	for {
		ev := <-netNsEvent
		oldNetID, found := oc.VNIDMap[ev.Name]
		switch ev.Type {
		case api.Added:
			oc.VNIDMap[ev.Name] = ev.NetID
			// Skip this event if the old and new network ids are same
			if found && (oldNetID != ev.NetID) {
				err := oc.updatePodNetwork(ev.Name, ev.NetID, oldNetID)
				if err != nil {
					log.Errorf("Failed to update pod network for namespace '%s', error: %s", ev.Name, err)
				}
			}
		case api.Deleted:
			delete(oc.VNIDMap, ev.Name)
			if found && (oldNetID != vnid.GlobalVNID) {
				err := oc.updatePodNetwork(ev.Name, vnid.GlobalVNID, oldNetID)
				if err != nil {
					log.Errorf("Failed to update pod network for namespace '%s', error: %s", ev.Name, err)
				}
			}
		}
	}
}

func watchServices(oc *OvsController) {
	svcevent := make(chan *api.ServiceEvent)
	go oc.Registry.WatchServices(svcevent)
	for {
		ev := <-svcevent
		netid, found := oc.VNIDMap[ev.Service.Namespace]
		if !found {
			log.Errorf("Error fetching Net ID for namespace: %s, skipped serviceEvent: %v", ev.Service.Namespace, ev)
		}
		switch ev.Type {
		case api.Added:
			oc.services[ev.Service.UID] = ev.Service
			for _, port := range ev.Service.Ports {
				oc.flowController.AddServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
			}
		case api.Deleted:
			delete(oc.services, ev.Service.UID)
			for _, port := range ev.Service.Ports {
				oc.flowController.DelServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
			}
		case api.Modified:
			oldsvc, exists := oc.services[ev.Service.UID]
			if exists && len(oldsvc.Ports) == len(ev.Service.Ports) {
				same := true
				for i := range oldsvc.Ports {
					if oldsvc.Ports[i].Protocol != ev.Service.Ports[i].Protocol || oldsvc.Ports[i].Port != ev.Service.Ports[i].Port {
						same = false
						break
					}
				}
				if same {
					continue
				}
			}
			if exists {
				for _, port := range oldsvc.Ports {
					oc.flowController.DelServiceOFRules(netid, oldsvc.IP, port.Protocol, port.Port)
				}
			}
			oc.services[ev.Service.UID] = ev.Service
			for _, port := range ev.Service.Ports {
				oc.flowController.AddServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
			}
		}
	}
}

func watchPods(oc *OvsController) {
	oc.Registry.WatchPods()
}
