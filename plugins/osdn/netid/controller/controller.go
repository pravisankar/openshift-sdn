package controller

import (
	"fmt"

	"github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/util/sets"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
)

type Allocation struct {
	vnid   vnidallocator.Interface
	client kclient.NamespaceInterface
	// globalNamespaces are the namespaces that have access to all pods in the cluster and vice versa.
	globalNamespaces sets.String
}

// retryCount is the number of times to retry on a conflict when updating a namespace
const retryCount = 2

// AddOrUpdate allocates network id(VNID) for the namespace if needed
func (c *Allocation) AddOrUpdate(ns *kapi.Namespace) error {
	tx := &tx{}
	defer tx.Rollback()

	var id uint
	userRequestedVnid := false
	value, err := netid.GetWantsVNID(ns)
	if err == nil {
		id = value
		userRequestedVnid = true
		netid.DeleteWantsVNID(ns)
	} else if err != netid.ErrorVNIDNotFound {
		return err
	} else if _, err = netid.GetVNID(ns); err == nil {
		// Nothing to do in this case
		// VNID already allocated and namespace does not want new VNID
		return nil
	}

	name := ns.ObjectMeta.Name
	if userRequestedVnid {
		// Only accept global VNID or VNIDs that are already allocated
		if (id != netid.GlobalVNID) && !c.vnid.Has(id) {
			return fmt.Errorf("requested netid %d not allocated", id)
		}
	} else {
		// Do vnid allocation
		if c.globalNamespaces.Has(name) {
			id = netid.GlobalVNID
		} else {
			id, err = c.vnid.AllocateNext()
			if err != nil {
				return err
			}
			tx.Add(func() error { return c.vnid.Release(id) })
		}
	}
	if err := netid.SetVNID(ns, id); err != nil {
		return err
	}

	// TODO: could use a client.GuaranteedUpdate/Merge function
	for i := 0; i < retryCount; i++ {
		_, err := c.client.Update(ns)
		if err == nil {
			if userRequestedVnid {
				glog.Infof("Updated netid %d for namespace %q", id, name)
			} else {
				glog.Infof("Assigned netid %d for namespace %q", id, name)
			}
			// commit and exit
			tx.Commit()
			return nil
		}

		if errors.IsNotFound(err) {
			return nil
		}
		if !errors.IsConflict(err) {
			return err
		}
		newNs, err := c.client.Get(name)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		newid, err := netid.GetVNID(newNs)
		if (err == nil) && (newid != id) {
			return fmt.Errorf("netid for namespace %q changed to %d on the flight, so can't assign netid %d", name, newid, id)
		}

		// try again
		if err := netid.SetVNID(newNs, id); err != nil {
			return err
		}
		ns = newNs
	}

	return fmt.Errorf("unable to allocate vnid for namespace %q after %d retries", name, retryCount)
}

// Delete releases the network id(VNID) for the namespace
func (c *Allocation) Delete(ns *kapi.Namespace) error {
	id, err := netid.GetVNID(ns)
	if err != nil {
		return nil
	}

	if id == netid.GlobalVNID {
		return nil
	}

	// TODO: use namespace cache
	// Do VNID Release if needed
	nsList, err := c.client.List(kapi.ListOptions{})
	if err != nil {
		return err
	}
	name := ns.ObjectMeta.Name
	for _, n := range nsList.Items {
		// Skip current namespace
		if n.ObjectMeta.Name == name {
			continue
		}
		// Skip namespaces that doesn't match given id
		if value, err := netid.GetVNID(&n); (err != nil) || (value != id) {
			continue
		}
		// VNID is in use by other namespace
		glog.Infof("Ignored releasing netid %d for namespace %q (still in use by %q)", id, name, n.ObjectMeta.Name)
		return nil
	}

	err = c.vnid.Release(id)
	if err != nil {
		return err
	}
	glog.Infof("Released netid %d for namespace %q", id, name)
	return nil
}

type tx struct {
	rollback []func() error
}

func (tx *tx) Add(fn func() error) {
	tx.rollback = append(tx.rollback, fn)
}

func (tx *tx) Rollback() {
	for _, fn := range tx.rollback {
		if err := fn(); err != nil {
			utilruntime.HandleError(fmt.Errorf("unable to undo tx: %v", err))
		}
	}
}

func (tx *tx) Commit() {
	tx.rollback = nil
}
