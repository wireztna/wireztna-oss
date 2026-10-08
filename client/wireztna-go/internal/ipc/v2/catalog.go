package v2

import (
	"context"
	"errors"
	"fmt"

	"github.com/wireztna/client/desktop/controller"
)

// CatalogProvider returns the current authenticated, non-secret connection catalog.
type CatalogProvider interface {
	ConnectionCatalog(context.Context) (ConnectionCatalog, error)
}

// ConnectionCatalog is the authenticated read model used to select access.
type ConnectionCatalog struct {
	ConfigurationID      string            `json:"configuration_id"`
	Projects             []CatalogProject  `json:"projects"`
	ExitNodes            []CatalogExitNode `json:"exit_nodes"`
	HasOverlap           bool              `json:"has_overlap"`
	SelectionRecommended bool              `json:"selection_recommended"`
	AllowAll             bool              `json:"allow_all"`
	VPNMode              bool              `json:"vpn_mode"`
}

// CatalogProject is one selectable access group and its non-secret resources.
type CatalogProject struct {
	GroupID         string            `json:"group_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	OnlineResources int               `json:"online_resources"`
	CIDRs           []string          `json:"cidrs"`
	Resources       []CatalogResource `json:"resources"`
}

// CatalogResource is one publisher visible through a project.
type CatalogResource struct {
	PublisherID  string   `json:"publisher_id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	ExposedCIDRs []string `json:"exposed_cidrs"`
}

// CatalogExitNode is one selectable full-tunnel publisher.
type CatalogExitNode struct {
	ExitNodeID string `json:"exit_node_id"`
	Name       string `json:"name"`
	Location   string `json:"location,omitempty"`
	Status     string `json:"status"`
}

// SelectionPayload lets a capable client select access while the service owns
// configuration identity and generation allocation.
type SelectionPayload struct {
	GroupID    string `json:"group_id"`
	ExitNodeID string `json:"exit_node_id,omitempty"`
}

func (p SelectionPayload) validate() error {
	if p.GroupID == "" {
		return errors.New("group_id is required")
	}
	return nil
}

func (c ConnectionCatalog) validate() error {
	if c.ConfigurationID == "" {
		return errors.New("catalog configuration_id is required")
	}
	groups := make(map[string]struct{}, len(c.Projects))
	for _, project := range c.Projects {
		if project.GroupID == "" {
			return errors.New("catalog project group_id is required")
		}
		if _, duplicate := groups[project.GroupID]; duplicate {
			return fmt.Errorf("catalog contains duplicate group_id %q", project.GroupID)
		}
		groups[project.GroupID] = struct{}{}
		resources := make(map[string]struct{}, len(project.Resources))
		for _, resource := range project.Resources {
			if resource.PublisherID == "" {
				return errors.New("catalog resource publisher_id is required")
			}
			if _, duplicate := resources[resource.PublisherID]; duplicate {
				return fmt.Errorf("catalog project %q contains a duplicate publisher_id", project.GroupID)
			}
			resources[resource.PublisherID] = struct{}{}
		}
	}
	exitNodes := make(map[string]struct{}, len(c.ExitNodes))
	for _, exitNode := range c.ExitNodes {
		if exitNode.ExitNodeID == "" {
			return errors.New("catalog exit_node_id is required")
		}
		if _, duplicate := exitNodes[exitNode.ExitNodeID]; duplicate {
			return fmt.Errorf("catalog contains duplicate exit_node_id %q", exitNode.ExitNodeID)
		}
		exitNodes[exitNode.ExitNodeID] = struct{}{}
	}
	return nil
}

func (c ConnectionCatalog) validateSelection(selection SelectionPayload) error {
	if err := selection.validate(); err != nil {
		return err
	}
	var selectedProject *CatalogProject
	for index := range c.Projects {
		if c.Projects[index].GroupID == selection.GroupID {
			selectedProject = &c.Projects[index]
			break
		}
	}
	if selectedProject == nil {
		return errors.New("selected group_id is not in the current catalog")
	}
	if selection.ExitNodeID == "" {
		return nil
	}
	exitFound := false
	for _, exitNode := range c.ExitNodes {
		if exitNode.ExitNodeID == selection.ExitNodeID {
			exitFound = true
			if exitNode.Status != "online" {
				return errors.New("selected exit_node_id is not online")
			}
			break
		}
	}
	if !exitFound {
		return errors.New("selected exit_node_id is not in the current catalog")
	}
	for _, resource := range selectedProject.Resources {
		if resource.PublisherID == selection.ExitNodeID {
			return nil
		}
	}
	return errors.New("selected exit_node_id is not accessible through the selected group")
}

func pristineSnapshot(snapshot controller.Snapshot) bool {
	return snapshot.State == controller.ConnectionStateDisconnected &&
		snapshot.ActiveOperation == nil && snapshot.Sequence == 0 &&
		snapshot.Desired == (controller.DesiredState{}) &&
		snapshot.Applied == (controller.AppliedState{})
}
