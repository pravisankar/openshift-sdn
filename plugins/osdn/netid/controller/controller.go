package controller

import (
	"fmt"
	"time"

	"github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	kcache "k8s.io/kubernetes/pkg/client/cache"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/runtime"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/util/sets"
	utilwait "k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
	"github.com/openshift/origin/pkg/client/cache"
	"github.com/openshift/origin/pkg/controller"
)

type VnidController struct {
	vnid   vnidallocator.Interface
	client kclient.NamespaceInterface
	Queue  *cache.EventQueue

	// globalNamespaces are the namespaces that have access to all pods in the cluster and vice versa.
	globalNamespaces sets.String
}

func NewVnidController(vnida vnidallocator.Interface, client kclient.NamespaceInterface, globalNamespaces []string) *VnidController {
	return &VnidController{
		vnid:             vnida,
		client:           client,
		globalNamespaces: sets.NewString(globalNamespaces...),
	}
}

// Create creates a vnid controller
func (c *VnidController) Create() controller.RunnableController {
	if c.Queue == nil {
		lw := &kcache.ListWatch{
			ListFunc: func(options kapi.ListOptions) (runtime.Object, error) {
				return c.client.List(options)
			},
			WatchFunc: func(options kapi.ListOptions) (watch.Interface, error) {
				return c.client.Watch(options)
			},
		}
		q := cache.NewEventQueue(kcache.MetaNamespaceKeyFunc)
		kcache.NewReflector(lw, &kapi.Namespace{}, q, 10*time.Minute).Run()
		c.Queue = q
	}
	return c
}

// Run begins processing resources from Queue asynchronously.
func (c *VnidController) Run() {
	go utilwait.Forever(func() { c.handleOne(c.Queue.Pop()) }, 0)
}

// handleOne processes resource with Handle.
func (c *VnidController) handleOne(eventType watch.EventType, resource interface{}, err error) {
	if err != nil {
		glog.Errorf("Failed to pop namespace from event queue: %v", err)
		return
	}

	ns := resource.(*kapi.Namespace)
	switch eventType {
	case watch.Added, watch.Modified:
		err = c.addOrUpdate(ns)
	case watch.Deleted:
		err = c.delete(ns)
	}
	if err != nil {
		glog.Errorf("Failed to apply vnid allocation, event-type: %v, namespace: %v, error: %v", eventType, ns, err)
	}
}

// retryCount is the number of times to retry on a conflict when updating a namespace
const retryCount = 2

// addOrUpdate allocates network id(VNID) for the namespace if needed
func (c *VnidController) addOrUpdate(ns *kapi.Namespace) error {
	tx := &tx{}
	defer tx.Rollback()

	var id uint
	userRequestedVnid := false
	value, err := netid.GetRequestedVNID(ns)
	if err == nil {
		id = value
		userRequestedVnid = true
		netid.DeleteRequestedVNID(ns)
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

		// Ignore if the namespace does not exist anymore
		if errors.IsNotFound(err) {
			return nil
		}
		if !errors.IsConflict(err) {
			return err
		}

		newNs, err := c.client.Get(name)
		// Ignore if the namespace does not exist anymore
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

// delete releases the network id(VNID) for the namespace
func (c *VnidController) delete(ns *kapi.Namespace) error {
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
