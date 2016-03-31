package controller

import (
	"time"

	"github.com/golang/glog"
	kapi "k8s.io/kubernetes/pkg/api"
	kcache "k8s.io/kubernetes/pkg/client/cache"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/sets"
	utilwait "k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"

	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
	"github.com/openshift/origin/pkg/client/cache"
	"github.com/openshift/origin/pkg/controller"
)

// AllocationFactory can create an Allocation controller.
type AllocationFactory struct {
	VNIDAllocator vnidallocator.Interface
	Client        kclient.NamespaceInterface
	Queue         *cache.EventQueue

	// GlobalNamespaces are the namespaces that have access to all pods in the cluster and vice versa.
	GlobalNamespaces []string
}

func NewAllocationFactory(vnida vnidallocator.Interface, client kclient.NamespaceInterface, globalNamespaces []string) *AllocationFactory {
	return &AllocationFactory{
		VNIDAllocator:    vnida,
		Client:           client,
		GlobalNamespaces: globalNamespaces,
	}
}

// Create creates a Allocation.
func (f *AllocationFactory) Create() controller.RunnableController {
	if f.Queue == nil {
		lw := &kcache.ListWatch{
			ListFunc: func(options kapi.ListOptions) (runtime.Object, error) {
				return f.Client.List(options)
			},
			WatchFunc: func(options kapi.ListOptions) (watch.Interface, error) {
				return f.Client.Watch(options)
			},
		}
		q := cache.NewEventQueue(kcache.MetaNamespaceKeyFunc)
		kcache.NewReflector(lw, &kapi.Namespace{}, q, 10*time.Minute).Run()
		f.Queue = q
	}
	return f
}

// Run begins processing resources from Queue asynchronously.
func (f *AllocationFactory) Run() {
	go utilwait.Forever(func() { f.handleOne(f.Queue.Pop()) }, 0)
}

// handleOne processes resource with Handle.
func (f *AllocationFactory) handleOne(eventType watch.EventType, resource interface{}, err error) {
	if err != nil {
		glog.Errorf("Failed to pop namespace from event queue: %v", err)
		return
	}

	c := &Allocation{
		vnid:             f.VNIDAllocator,
		client:           f.Client,
		globalNamespaces: sets.NewString(f.GlobalNamespaces...),
	}

	ns := resource.(*kapi.Namespace)
	switch eventType {
	case watch.Added, watch.Modified:
		err = c.AddOrUpdate(ns)
	case watch.Deleted:
		err = c.Delete(ns)
	}
	if err != nil {
		glog.Errorf("Failed to apply vnid allocation, event-type: %v, namespace: %v, error: %v", eventType, ns, err)
	}
}
