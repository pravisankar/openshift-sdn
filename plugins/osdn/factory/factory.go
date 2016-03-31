package factory

import (
	"strings"

	osclient "github.com/openshift/origin/pkg/client"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/storage"

	"github.com/openshift/openshift-sdn/plugins/osdn"
	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	"github.com/openshift/openshift-sdn/plugins/osdn/ovs"
)

// Call by higher layers to create the plugin instance
func NewPlugin(pluginType string, osClient *osclient.Client, kClient *kclient.Client, etcdHelper storage.Interface, hostname string, selfIP string) (api.OsdnPlugin, api.FilteringEndpointsConfigHandler, error) {
	switch strings.ToLower(pluginType) {
	case ovs.SingleTenantPluginName:
		return ovs.CreatePlugin(osdn.NewRegistry(osClient, kClient), etcdHelper, false, hostname, selfIP)
	case ovs.MultiTenantPluginName:
		return ovs.CreatePlugin(osdn.NewRegistry(osClient, kClient), etcdHelper, true, hostname, selfIP)
	}

	return nil, nil, nil
}
