package network

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/meta"
	"k8s.io/kubernetes/pkg/api/unversioned"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/kubectl/resource"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	kerrors "k8s.io/kubernetes/pkg/util/errors"
	"k8s.io/kubernetes/pkg/util/sets"

	osclient "github.com/openshift/origin/pkg/client"
	"github.com/openshift/origin/pkg/cmd/util/clientcmd"
	"github.com/openshift/origin/pkg/project/api"
	sdnapi "github.com/openshift/origin/pkg/sdn/api"
)

const (
	ovsPluginName = "redhat/openshift-ovs-multitenant"
)

type ProjectOptions struct {
	DefaultNamespace string
	Oclient          *osclient.Client
	Kclient          *kclient.Client
	Out              io.Writer

	Mapper            meta.RESTMapper
	Typer             runtime.ObjectTyper
	RESTClientFactory func(mapping *meta.RESTMapping) (resource.RESTClient, error)

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
	oc, kc, err := f.Clients()
	if err != nil {
		return err
	}
	mapper, typer := f.Object()

	p.DefaultNamespace = defaultNamespace
	p.Oclient = oc
	p.Kclient = kc
	p.Out = out
	p.Mapper = mapper
	p.Typer = typer
	p.RESTClientFactory = f.Factory.RESTClient
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

func (p *ProjectOptions) GetProjects() ([]*api.Project, error) {
	nameArgs := []string{"projects"}
	if len(p.ProjectNames) != 0 {
		nameArgs = append(nameArgs, p.ProjectNames...)
	}

	r := resource.NewBuilder(p.Mapper, p.Typer, resource.ClientMapperFunc(p.RESTClientFactory)).
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
	projectList := []*api.Project{}
	_ = r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		project, ok := info.Object.(*api.Project)
		if !ok {
			err := fmt.Errorf("cannot convert input to Project: %v", reflect.TypeOf(info.Object))
			errList = append(errList, err)
			// Don't bail out if one project fails
			return nil
		}
		projectList = append(projectList, project)
		return nil
	})
	if len(errList) != 0 {
		return projectList, kerrors.NewAggregate(errList)
	}

	if len(projectList) == 0 {
		return projectList, fmt.Errorf("No projects found")
	} else {
		givenProjectNames := sets.NewString(p.ProjectNames...)
		foundProjectNames := sets.String{}
		for _, project := range projectList {
			foundProjectNames.Insert(project.ObjectMeta.Name)
		}
		skippedProjectNames := givenProjectNames.Difference(foundProjectNames)
		if skippedProjectNames.Len() > 0 {
			return projectList, fmt.Errorf("Projects %v not found", strings.Join(skippedProjectNames.List(), ", "))
		}
	}
	return projectList, nil
}

func (p *ProjectOptions) GetProjectNetworks() (*sdnapi.ProjectNetworkList, error) {
	networks, err := p.Oclient.ProjectNetworks().List()
	if err != nil {
		return nil, err
	}
	return networks, nil
}

func (p *ProjectOptions) GetNetID(name string) (uint, error) {
	var netID uint
	networks, err := p.GetProjectNetworks()
	if err != nil {
		return netID, err
	}

	for _, net := range networks.Items {
		if name == net.ObjectMeta.Name {
			return *net.NetID, nil
		}
	}
	return netID, fmt.Errorf("Net ID not found for project: %s", name)
}

func (p *ProjectOptions) CreateNewNetID(name string) error {
	projNet, err := p.Oclient.ProjectNetworks().Get(name)
	if err != nil {
		// Create project network
		projNet := newProjectNetwork(name)
		_, err = p.Oclient.ProjectNetworks().Create(projNet)
	} else {
		projNet.NetID = nil
		_, err = p.Oclient.ProjectNetworks().Update(projNet)
	}
	return err
}

func (p *ProjectOptions) CreateOrUpdateProjectNetwork(name string, id uint) error {
	projNet, err := p.Oclient.ProjectNetworks().Get(name)
	if err != nil {
		// Create project network
		projNet := newProjectNetwork(name)
		projNet.NetID = &id
		_, err = p.Oclient.ProjectNetworks().Create(projNet)
	} else if *projNet.NetID != id {
		// Update project network
		projNet.NetID = &id
		_, err = p.Oclient.ProjectNetworks().Update(projNet)
	}
	return err
}

func newProjectNetwork(name string) *sdnapi.ProjectNetwork {
	return &sdnapi.ProjectNetwork{
		TypeMeta:   unversioned.TypeMeta{Kind: "ProjectNetwork"},
		ObjectMeta: kapi.ObjectMeta{Name: name},
		NetName:    name,
	}
}
