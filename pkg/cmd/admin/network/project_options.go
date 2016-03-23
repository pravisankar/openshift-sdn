package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	kapi "k8s.io/kubernetes/pkg/api"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/kubectl/resource"
	"k8s.io/kubernetes/pkg/labels"
	kerrors "k8s.io/kubernetes/pkg/util/errors"
	"k8s.io/kubernetes/pkg/util/sets"
	"k8s.io/kubernetes/pkg/util/strategicpatch"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/origin/pkg/cmd/util/clientcmd"
)

const (
	ovsPluginName string = "redhat/openshift-ovs-multitenant"
)

type ProjectOptions struct {
	DefaultNamespace string
	Kclient          *kclient.Client
	factory          *clientcmd.Factory

	ProjectNames []string

	// Common optional params
	Selector      string
	CheckSelector bool
}

func (p *ProjectOptions) Complete(f *clientcmd.Factory, c *cobra.Command, args []string, out io.Writer) error {
	defaultNamespace, _, err := f.DefaultNamespace()
	if err != nil {
		return err
	}
	_, kc, err := f.Clients()
	if err != nil {
		return err
	}

	p.DefaultNamespace = defaultNamespace
	p.Kclient = kc
	p.factory = f
	p.ProjectNames = []string{}
	if len(args) != 0 {
		p.ProjectNames = append(p.ProjectNames, args...)
	}
	return nil
}

// Common validations
func (p *ProjectOptions) Validate() error {
	errList := []error{}
	if p.CheckSelector {
		if len(p.Selector) > 0 {
			if _, err := labels.Parse(p.Selector); err != nil {
				errList = append(errList, errors.New("--selector=<project_selector> must be a valid label selector"))
			}
		}
		if len(p.ProjectNames) != 0 {
			errList = append(errList, errors.New("either specify --selector=<project_selector> or projects but not both"))
		}
	} else if len(p.ProjectNames) == 0 {
		errList = append(errList, errors.New("must provide --selector=<project_selector> or projects"))
	}

	// TODO: Validate if the openshift master is running with mutitenant network plugin
	return kerrors.NewAggregate(errList)
}

func (p *ProjectOptions) GetNamespacesInfo() ([]*resource.Info, error) {
	nameArgs := []string{"namespaces"}
	if len(p.ProjectNames) != 0 {
		nameArgs = append(nameArgs, p.ProjectNames...)
	}

	mapper, typer := p.factory.Object(false)
	r := resource.NewBuilder(mapper, typer, resource.ClientMapperFunc(p.factory.ClientForMapping), p.factory.Decoder(true)).
		ContinueOnError().
		NamespaceParam(p.DefaultNamespace).
		SelectorParam(p.Selector).
		ResourceTypeOrNameArgs(true, nameArgs...).
		Flatten().
		Do()
	if r.Err() != nil {
		return nil, r.Err()
	}

	errList := []error{}
	infoList := []*resource.Info{}
	_ = r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		_, ok := info.Object.(*kapi.Namespace)
		if !ok {
			err := fmt.Errorf("cannot convert input to Namespace: %v", reflect.TypeOf(info.Object))
			errList = append(errList, err)
			// Don't bail out if one namespace fails
			return nil
		}
		infoList = append(infoList, info)
		return nil
	})
	if len(errList) != 0 {
		return infoList, kerrors.NewAggregate(errList)
	}

	if len(infoList) == 0 {
		return infoList, fmt.Errorf("No projects found")
	} else {
		givenProjectNames := sets.NewString(p.ProjectNames...)
		foundProjectNames := sets.String{}
		for _, info := range infoList {
			ns, _ := info.Object.(*kapi.Namespace)
			foundProjectNames.Insert(ns.ObjectMeta.Name)
		}
		skippedProjectNames := givenProjectNames.Difference(foundProjectNames)
		if skippedProjectNames.Len() > 0 {
			return infoList, fmt.Errorf("Projects %v not found", strings.Join(skippedProjectNames.List(), ", "))
		}
	}
	return infoList, nil
}

func (p *ProjectOptions) GetNetID(name string) (uint, error) {
	ns, err := p.Kclient.Namespaces().Get(name)
	if err != nil {
		return math.MaxUint32, err
	}

	return netid.GetVNID(ns)
}

func (p *ProjectOptions) validateNetID(name string, id uint) error {
	// Timeout: 10 secs
	retries := 20
	retryInterval := 500 * time.Millisecond

	for i := 0; i < retries; i++ {
		ns, err := p.Kclient.Namespaces().Get(name)
		if err != nil {
			return err
		}
		curID, er := netid.GetVNID(ns)
		if (er == nil) && (curID == id) {
			return nil
		}
		time.Sleep(retryInterval)
	}
	return fmt.Errorf("Failed to apply netid %d for project %q", id, name)
}

func (p *ProjectOptions) UpdateNamespace(info *resource.Info, id uint) error {
	ns, ok := info.Object.(*kapi.Namespace)
	if !ok {
		return fmt.Errorf("Invalid resource info: %v", info)
	}
	oldData, err := json.Marshal(info.Object)
	if err != nil {
		return err
	}
	if err := netid.SetWantsVNID(ns, id); err != nil {
		return err
	}

	newData, err := json.Marshal(info.Object)
	if err != nil {
		return err
	}
	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, info.Object)
	if err != nil {
		return fmt.Errorf("couldn't compute namespace patch: %v", err)
	}

	mapping := info.ResourceMapping()
	client, err := p.factory.ClientForMapping(mapping)
	if err != nil {
		return err
	}
	helper := resource.NewHelper(client, mapping)

	_, err = helper.Patch(ns.ObjectMeta.Namespace, ns.ObjectMeta.Name, kapi.StrategicMergePatchType, patchBytes)
	if err != nil {
		return err
	}
	return p.validateNetID(ns.ObjectMeta.Name, id)
}
