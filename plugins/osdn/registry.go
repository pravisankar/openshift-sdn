package osdn

import (
	"net"
	"strings"

	log "github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/client/cache"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	pconfig "k8s.io/kubernetes/pkg/proxy/config"
	"k8s.io/kubernetes/pkg/types"
	"k8s.io/kubernetes/pkg/watch"

	"github.com/openshift/openshift-sdn/pkg/netutils"
	osdnapi "github.com/openshift/openshift-sdn/plugins/osdn/api"

	osclient "github.com/openshift/origin/pkg/client"
	oscache "github.com/openshift/origin/pkg/client/cache"
	originapi "github.com/openshift/origin/pkg/sdn/api"
)

type Registry struct {
	oClient *osclient.Client
	kClient *kclient.Client

	baseEndpointsHandler pconfig.EndpointsConfigHandler
	serviceNetwork       *net.IPNet
	clusterNetwork       *net.IPNet
	namespaceOfPodIP     map[string]string
}

func NewRegistry(osClient *osclient.Client, kClient *kclient.Client) *Registry {
	var clusterNetwork, serviceNetwork *net.IPNet
	cn, err := osClient.ClusterNetwork().Get("default")
	if err == nil {
		_, clusterNetwork, _ = net.ParseCIDR(cn.Network)
		_, serviceNetwork, _ = net.ParseCIDR(cn.ServiceNetwork)
	}
	// else the same error will occur again later and be reported

	return &Registry{
		oClient:          osClient,
		kClient:          kClient,
		serviceNetwork:   serviceNetwork,
		clusterNetwork:   clusterNetwork,
		namespaceOfPodIP: make(map[string]string),
	}
}

func (registry *Registry) GetSubnets() ([]osdnapi.Subnet, error) {
	hostSubnetList, err := registry.oClient.HostSubnets().List()
	if err != nil {
		return nil, err
	}
	// convert HostSubnet to osdnapi.Subnet
	subList := make([]osdnapi.Subnet, 0, len(hostSubnetList.Items))
	for _, subnet := range hostSubnetList.Items {
		subList = append(subList, osdnapi.Subnet{NodeIP: subnet.HostIP, SubnetCIDR: subnet.Subnet})
	}
	return subList, nil
}

func (registry *Registry) GetSubnet(nodeName string) (*osdnapi.Subnet, error) {
	hs, err := registry.oClient.HostSubnets().Get(nodeName)
	if err != nil {
		return nil, err
	}
	return &osdnapi.Subnet{NodeIP: hs.HostIP, SubnetCIDR: hs.Subnet}, nil
}

func (registry *Registry) DeleteSubnet(nodeName string) error {
	return registry.oClient.HostSubnets().Delete(nodeName)
}

func (registry *Registry) CreateSubnet(nodeName string, sub *osdnapi.Subnet) error {
	hs := &originapi.HostSubnet{
		TypeMeta:   unversioned.TypeMeta{Kind: "HostSubnet"},
		ObjectMeta: kapi.ObjectMeta{Name: nodeName},
		Host:       nodeName,
		HostIP:     sub.NodeIP,
		Subnet:     sub.SubnetCIDR,
	}
	_, err := registry.oClient.HostSubnets().Create(hs)
	return err
}

func (registry *Registry) WatchSubnets(receiver chan<- *osdnapi.SubnetEvent) error {
	eventQueue := registry.runEventQueue("HostSubnets")

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		hs := obj.(*originapi.HostSubnet)

		switch eventType {
		case watch.Added, watch.Modified:
			receiver <- &osdnapi.SubnetEvent{Type: osdnapi.Added, NodeName: hs.Host, Subnet: osdnapi.Subnet{NodeIP: hs.HostIP, SubnetCIDR: hs.Subnet}}
		case watch.Deleted:
			receiver <- &osdnapi.SubnetEvent{Type: osdnapi.Deleted, NodeName: hs.Host, Subnet: osdnapi.Subnet{NodeIP: hs.HostIP, SubnetCIDR: hs.Subnet}}
		}
	}
}

func newSDNPod(kPod *kapi.Pod) osdnapi.Pod {
	containerID := ""
	if len(kPod.Status.ContainerStatuses) > 0 {
		// Extract only container ID, pod.Status.ContainerStatuses[0].ContainerID is of the format: docker://<containerID>
		if parts := strings.Split(kPod.Status.ContainerStatuses[0].ContainerID, "://"); len(parts) > 1 {
			containerID = parts[1]
		}
	}
	return osdnapi.Pod{
		Name:        kPod.ObjectMeta.Name,
		Namespace:   kPod.ObjectMeta.Namespace,
		ContainerID: containerID,
	}
}

func (registry *Registry) WatchPods() error {
	eventQueue := registry.runEventQueue("Pods")

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		kPod := obj.(*kapi.Pod)

		switch eventType {
		case watch.Added, watch.Modified:
			if kPod.Status.PodIP != "" {
				registry.namespaceOfPodIP[kPod.Status.PodIP] = kPod.ObjectMeta.Namespace
			}
		case watch.Deleted:
			delete(registry.namespaceOfPodIP, kPod.Status.PodIP)
		}
	}
}

func (registry *Registry) GetRunningPods(nodeName, namespace string) ([]osdnapi.Pod, error) {
	fieldSelector := fields.Set{"spec.host": nodeName}.AsSelector()
	podList, err := registry.kClient.Pods(namespace).List(labels.Everything(), fieldSelector)
	if err != nil {
		return nil, err
	}

	// Filter running pods and convert kapi.Pod to osdnapi.Pod
	pods := make([]osdnapi.Pod, 0, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.Status.Phase == kapi.PodRunning {
			pods = append(pods, newSDNPod(&pod))
		}
	}
	return pods, nil
}

func (registry *Registry) getNodeAddressMap() (map[types.UID]string, error) {
	nodeAddressMap := map[types.UID]string{}

	nodes, err := registry.kClient.Nodes().List(labels.Everything(), fields.Everything())
	if err != nil {
		return nodeAddressMap, err
	}
	for _, node := range nodes.Items {
		if len(node.Status.Addresses) > 0 {
			nodeAddressMap[node.ObjectMeta.UID] = node.Status.Addresses[0].Address
		}
	}
	return nodeAddressMap, nil
}

func (registry *Registry) WatchNodes(receiver chan<- *osdnapi.NodeEvent) error {
	eventQueue := registry.runEventQueue("Nodes")

	nodeAddressMap, err := registry.getNodeAddressMap()
	if err != nil {
		return err
	}

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		node := obj.(*kapi.Node)

		nodeIP := ""
		if len(node.Status.Addresses) > 0 {
			nodeIP = node.Status.Addresses[0].Address
		} else {
			nodeIP, err = netutils.GetNodeIP(node.ObjectMeta.Name)
			if err != nil {
				return err
			}
		}

		switch eventType {
		case watch.Added:
			receiver <- &osdnapi.NodeEvent{Type: osdnapi.Added, Node: osdnapi.Node{Name: node.ObjectMeta.Name, IP: nodeIP}}
			nodeAddressMap[node.ObjectMeta.UID] = nodeIP
		case watch.Modified:
			oldNodeIP, ok := nodeAddressMap[node.ObjectMeta.UID]
			if ok && oldNodeIP != nodeIP {
				// Node Added event will handle update subnet if there is ip mismatch
				receiver <- &osdnapi.NodeEvent{Type: osdnapi.Added, Node: osdnapi.Node{Name: node.ObjectMeta.Name, IP: nodeIP}}
				nodeAddressMap[node.ObjectMeta.UID] = nodeIP
			}
		case watch.Deleted:
			receiver <- &osdnapi.NodeEvent{Type: osdnapi.Deleted, Node: osdnapi.Node{Name: node.ObjectMeta.Name}}
			delete(nodeAddressMap, node.ObjectMeta.UID)
		}
	}
}

func (registry *Registry) WriteNetworkConfig(network string, subnetLength uint, serviceNetwork string) error {
	cn, err := registry.oClient.ClusterNetwork().Get("default")
	if err == nil {
		if cn.Network == network && cn.HostSubnetLength == int(subnetLength) && cn.ServiceNetwork == serviceNetwork {
			return nil
		}

		cn.Network = network
		cn.HostSubnetLength = int(subnetLength)
		cn.ServiceNetwork = serviceNetwork
		_, err = registry.oClient.ClusterNetwork().Update(cn)
		return err
	}
	cn = &originapi.ClusterNetwork{
		TypeMeta:         unversioned.TypeMeta{Kind: "ClusterNetwork"},
		ObjectMeta:       kapi.ObjectMeta{Name: "default"},
		Network:          network,
		HostSubnetLength: int(subnetLength),
		ServiceNetwork:   serviceNetwork,
	}
	_, err = registry.oClient.ClusterNetwork().Create(cn)
	return err
}

func (registry *Registry) GetClusterNetworkCIDR() (string, error) {
	cn, err := registry.oClient.ClusterNetwork().Get("default")
	if err != nil {
		return "", err
	}
	return cn.Network, nil
}

func (registry *Registry) GetHostSubnetLength() (int, error) {
	cn, err := registry.oClient.ClusterNetwork().Get("default")
	if err != nil {
		return -1, err
	}
	return cn.HostSubnetLength, nil
}

func (registry *Registry) GetServicesNetworkCIDR() (string, error) {
	cn, err := registry.oClient.ClusterNetwork().Get("default")
	return cn.ServiceNetwork, err
}

func (registry *Registry) WatchNamespaces(receiver chan<- *osdnapi.NamespaceEvent) error {
	eventQueue := registry.runEventQueue("Namespaces")

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		ns := obj.(*kapi.Namespace)

		switch eventType {
		case watch.Added, watch.Modified:
			receiver <- &osdnapi.NamespaceEvent{Type: osdnapi.Added, Name: ns.ObjectMeta.Name}
		case watch.Deleted:
			receiver <- &osdnapi.NamespaceEvent{Type: osdnapi.Deleted, Name: ns.ObjectMeta.Name}
		}
	}
}

func (registry *Registry) WatchNetNamespaces(receiver chan<- *osdnapi.NetNamespaceEvent) error {
	eventQueue := registry.runEventQueue("NetNamespaces")

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		netns := obj.(*originapi.NetNamespace)

		switch eventType {
		case watch.Added, watch.Modified:
			receiver <- &osdnapi.NetNamespaceEvent{Type: osdnapi.Added, Name: netns.NetName, NetID: *netns.NetID}
		case watch.Deleted:
			receiver <- &osdnapi.NetNamespaceEvent{Type: osdnapi.Deleted, Name: netns.NetName}
		}
	}
}

func (registry *Registry) GetNetNamespace(name string) (osdnapi.NetNamespace, error) {
	netns, err := registry.oClient.NetNamespaces().Get(name)
	if err != nil {
		return osdnapi.NetNamespace{}, err
	}
	return osdnapi.NetNamespace{Name: netns.Name, NetID: *netns.NetID}, nil
}

func (registry *Registry) CreateNetNamespace(name string) error {
	netns := &originapi.NetNamespace{
		TypeMeta:   unversioned.TypeMeta{Kind: "NetNamespace"},
		ObjectMeta: kapi.ObjectMeta{Name: name},
		NetName:    name,
	}
	_, err := registry.oClient.NetNamespaces().Create(netns)
	return err
}

func (registry *Registry) DeleteNetNamespace(name string) error {
	return registry.oClient.NetNamespaces().Delete(name)
}

func (registry *Registry) GetServicesForNamespace(namespace string) ([]osdnapi.Service, error) {
	services, err := registry.getServices(namespace)
	return services, err
}

func (registry *Registry) GetServices() ([]osdnapi.Service, error) {
	return registry.getServices(kapi.NamespaceAll)
}

func (registry *Registry) getServices(namespace string) ([]osdnapi.Service, error) {
	kServList, err := registry.kClient.Services(namespace).List(labels.Everything(), fields.Everything())
	if err != nil {
		return nil, err
	}

	oServList := make([]osdnapi.Service, 0, len(kServList.Items))
	for _, kService := range kServList.Items {
		if !kapi.IsServiceIPSet(&kService) {
			continue
		}
		oServList = append(oServList, newSDNService(&kService))
	}
	return oServList, nil
}

func (registry *Registry) WatchServices(receiver chan<- *osdnapi.ServiceEvent) error {
	eventQueue := registry.runEventQueue("Services")

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			return err
		}
		kServ := obj.(*kapi.Service)

		// Ignore headless services
		if !kapi.IsServiceIPSet(kServ) {
			continue
		}

		switch eventType {
		case watch.Added:
			oServ := newSDNService(kServ)
			receiver <- &osdnapi.ServiceEvent{Type: osdnapi.Added, Service: oServ}
		case watch.Deleted:
			oServ := newSDNService(kServ)
			receiver <- &osdnapi.ServiceEvent{Type: osdnapi.Deleted, Service: oServ}
		case watch.Modified:
			oServ := newSDNService(kServ)
			receiver <- &osdnapi.ServiceEvent{Type: osdnapi.Modified, Service: oServ}
		}
	}
}

func newSDNService(kServ *kapi.Service) osdnapi.Service {
	ports := make([]osdnapi.ServicePort, len(kServ.Spec.Ports))
	for i, port := range kServ.Spec.Ports {
		ports[i] = osdnapi.ServicePort{osdnapi.ServiceProtocol(port.Protocol), uint(port.Port)}
	}

	return osdnapi.Service{
		Name:      kServ.ObjectMeta.Name,
		Namespace: kServ.ObjectMeta.Namespace,
		UID:       string(kServ.ObjectMeta.UID),
		IP:        kServ.Spec.ClusterIP,
		Ports:     ports,
	}
}

// Run event queue for the given resource
func (registry *Registry) runEventQueue(resourceName string) *oscache.EventQueue {
	var client cache.Getter
	var expectedType interface{}

	switch strings.ToLower(resourceName) {
	case "hostsubnets":
		expectedType = &originapi.HostSubnet{}
		client = registry.oClient
	case "netnamespaces":
		expectedType = &originapi.NetNamespace{}
		client = registry.oClient
	case "nodes":
		expectedType = &kapi.Node{}
		client = registry.kClient
	case "namespaces":
		expectedType = &kapi.Namespace{}
		client = registry.kClient
	case "services":
		expectedType = &kapi.Service{}
		client = registry.kClient
	case "pods":
		expectedType = &kapi.Pod{}
		client = registry.kClient
	default:
		log.Fatalf("Unknown resource %s during initialization of event queue", resourceName)
	}

	lw := cache.NewListWatchFromClient(client, strings.ToLower(resourceName), kapi.NamespaceAll, fields.Everything())
	eventQueue := oscache.NewEventQueue(cache.MetaNamespaceKeyFunc)
	cache.NewReflector(lw, expectedType, eventQueue, 0).Run()
	return eventQueue
}

// FilteringEndpointsConfigHandler implementation
func (registry *Registry) SetBaseEndpointsHandler(base pconfig.EndpointsConfigHandler) {
	registry.baseEndpointsHandler = base
}

func (registry *Registry) OnEndpointsUpdate(allEndpoints []kapi.Endpoints) {
	filteredEndpoints := make([]kapi.Endpoints, 0, len(allEndpoints))
EndpointLoop:
	for _, ep := range allEndpoints {
		ns := ep.ObjectMeta.Namespace
		for _, ss := range ep.Subsets {
			for _, addr := range ss.Addresses {
				IP := net.ParseIP(addr.IP)
				if registry.serviceNetwork.Contains(IP) {
					log.Warningf("Service '%s' in namespace '%s' has an Endpoint inside the service network (%s)", ep.ObjectMeta.Name, ns, addr.IP)
					continue EndpointLoop
				}
				if registry.clusterNetwork.Contains(IP) {
					podNamespace, ok := registry.namespaceOfPodIP[addr.IP]
					if !ok {
						log.Warningf("Service '%s' in namespace '%s' has an Endpoint pointing to non-existent pod (%s)", ep.ObjectMeta.Name, ns, addr.IP)
						continue EndpointLoop
					}
					if podNamespace != ns {
						log.Warningf("Service '%s' in namespace '%s' has an Endpoint pointing to pod %s in namespace '%s'", ep.ObjectMeta.Name, ns, addr.IP, podNamespace)
						continue EndpointLoop
					}
				}
			}
		}
		filteredEndpoints = append(filteredEndpoints, ep)
	}

	registry.baseEndpointsHandler.OnEndpointsUpdate(filteredEndpoints)
}
