package factory

import (
	"strings"
	"time"

	osclient "github.com/openshift/origin/pkg/client"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/storage"

	"github.com/openshift/openshift-sdn/plugins/osdn"
	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	"github.com/openshift/openshift-sdn/plugins/osdn/ovs"
)

// Call by higher layers to create the plugin SDN master instance
func NewMasterPlugin(pluginType string, osClient *osclient.Client, kClient *kclient.Client, etcdHelper storage.Interface) (api.OsdnPlugin, error) {
	return newPlugin(pluginType, osClient, kClient, etcdHelper, "", "", 0)
}

// Call by higher layers to create the plugin SDN node instance
func NewNodePlugin(pluginType string, osClient *osclient.Client, kClient *kclient.Client, hostname string, selfIP string, iptablesSyncPeriod time.Duration) (api.OsdnPlugin, error) {
	return newPlugin(pluginType, osClient, kClient, nil, hostname, selfIP, iptablesSyncPeriod)
}

func newPlugin(pluginType string, osClient *osclient.Client, kClient *kclient.Client, etcdHelper storage.Interface, hostname string, selfIP string, iptablesSyncPeriod time.Duration) (api.OsdnPlugin, error) {
	switch strings.ToLower(pluginType) {
	case ovs.SingleTenantPluginName:
		return ovs.CreatePlugin(osdn.NewRegistry(osClient, kClient), nil, false, hostname, selfIP, iptablesSyncPeriod)
	case ovs.MultiTenantPluginName:
		return ovs.CreatePlugin(osdn.NewRegistry(osClient, kClient), etcdHelper, true, hostname, selfIP, iptablesSyncPeriod)
	}

	return nil, nil
}

// Call by higher layers to create the proxy plugin instance; only used by nodes
func NewProxyPlugin(pluginType string, osClient *osclient.Client, kClient *kclient.Client) (api.FilteringEndpointsConfigHandler, error) {
	switch strings.ToLower(pluginType) {
	case ovs.MultiTenantPluginName:
		return ovs.CreateProxyPlugin(osdn.NewRegistry(osClient, kClient))
	}

	return nil, nil
}
