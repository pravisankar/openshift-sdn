package migration

import (
	"fmt"
	"path"

	"github.com/golang/glog"
	"golang.org/x/net/context"

	"k8s.io/kubernetes/pkg/api/errors"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/storage"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/origin/pkg/sdn/api"
)

const (
	etcdNetNamespacePrefix string = "/registry/sdnnetnamespaces"
)

type Migrate struct {
	etcdHelper storage.Interface
	client     kclient.NamespaceInterface
}

func NewMigrate(etcdHelper storage.Interface, client kclient.NamespaceInterface) *Migrate {
	return &Migrate{
		etcdHelper: etcdHelper,
		client:     client,
	}
}

func (m *Migrate) Run() error {
	var netnsList api.NetNamespaceList

	err := m.etcdHelper.List(context.TODO(), etcdNetNamespacePrefix, "", storage.Everything, &netnsList)
	if err != nil {
		return fmt.Errorf("List network namespaces failed: %v", err)
	}

	for _, netns := range netnsList.Items {
		// Get corresponding namespace for the NetNamespace object
		ns, err := m.client.Get(netns.NetName)
		if errors.IsNotFound(err) {
			m.deleteNetNamespace(&netns)
			continue
		}
		if err != nil {
			return fmt.Errorf("Get namespace %q failed: %v", netns.NetName, err)
		}

		// Set netid as namespace annotation
		if err := netid.SetVNID(ns, netns.NetID); err != nil {
			return err
		}

		// Update namespace
		updatedNs, err := m.client.Update(ns)
		if errors.IsNotFound(err) {
			m.deleteNetNamespace(&netns)
			continue
		}
		if err != nil {
			return err
		}

		// Validate
		if id, err := netid.GetVNID(updatedNs); err == nil {
			if id == netns.NetID {
				glog.Infof("Migrated netid %d from NetNamespace to annotation on namespace %q", netns.NetID, ns.Name)
			} else {
				return fmt.Errorf("Failed to migrate netid from NetNamespace to namespace %q, expected netid %d but got %d", ns.Name, netns.NetID, id)
			}
		} else {
			return fmt.Errorf("Failed to migrate netid %d from NetNamespace to annotation on namespace %q", netns.NetID, ns.Name)
		}

		// Delete processed NetNamespace object
		m.deleteNetNamespace(&netns)
	}
	return nil
}

func (m *Migrate) deleteNetNamespace(netns *api.NetNamespace) {
	var out api.NetNamespace
	if err := m.etcdHelper.Delete(context.TODO(), path.Join(etcdNetNamespacePrefix, netns.ObjectMeta.Name), &out, nil); err != nil {
		glog.Errorf("Failed to delete NetNamespace: %v, error: %v", netns, err)
	}
}
