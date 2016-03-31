package controller

import (
	"testing"

	kapi "k8s.io/kubernetes/pkg/api"
	ktestclient "k8s.io/kubernetes/pkg/client/unversioned/testclient"
	"k8s.io/kubernetes/pkg/runtime"

	"github.com/openshift/origin/pkg/client/testclient"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace/vnid"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace/vnidallocator"
)

func TestController(t *testing.T) {
	var action testclient.Action
	client := &testclient.Fake{}
	client.AddReactor("*", "*", func(a testclient.Action) (handled bool, ret runtime.Object, err error) {
		action = a
		return true, (*kapi.Namespace)(nil), nil
	})

	vnidr, err := vnid.NewVNIDRange(101, 10)
	if err != nil {
		t.Fatal(err)
	}
	vnida := vnidallocator.NewInMemory(vnidr)
	c := Allocation{
		vnid:   vnida,
		client: kclient.Namespaces(),
	}

	err = c.Next(&kapi.Namespace{ObjectMeta: kapi.ObjectMeta{Name: "test"}})
	if err != nil {
		t.Fatal(err)
	}

	got := action.(testclient.CreateAction).GetObject().(*kapi.Namespace)
	if *got.NetID != 101 {
		t.Errorf("unexpected vnid allocation: %#v", got)
	}
	if !vnida.Has(101) {
		t.Errorf("did not allocate vnid: %#v", vnida)
	}
}
