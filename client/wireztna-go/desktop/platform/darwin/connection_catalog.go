//go:build darwin

package darwin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	v2 "github.com/wireztna/client/internal/ipc/v2"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type availableGroupsAPI interface {
	GetAvailableGroupsContext(context.Context) (*api.AvailableGroupsResponse, error)
}

// ConnectionCatalogProvider reads the current authenticated access catalog.
// It retains only a stable digest of the enrolled public identity.
type ConnectionCatalogProvider struct {
	baseURL         string
	configurationID string
	tokens          TokenSource
	newClient       func(string, string) availableGroupsAPI
}

// NewConnectionCatalogProvider creates an inert provider. It derives the public
// key before hashing and never retains or exposes the enrolled private key.
func NewConnectionCatalogProvider(cfg *config.ClientConfig, tokens TokenSource) (*ConnectionCatalogProvider, error) {
	if cfg == nil || tokens == nil || cfg.APIURL == "" {
		return nil, errors.New("Darwin connection catalog configuration and token source are required")
	}
	privateKey, err := wgtypes.ParseKey(cfg.PrivateKey)
	if err != nil {
		return nil, errors.New("Darwin connection catalog identity is invalid")
	}
	publicIdentity := privateKey.PublicKey().String()
	digest := sha256.Sum256([]byte(publicIdentity))
	return &ConnectionCatalogProvider{
		baseURL:         cfg.APIURL,
		configurationID: "wg-public-sha256:" + hex.EncodeToString(digest[:]),
		tokens:          tokens,
		newClient: func(baseURL, token string) availableGroupsAPI {
			client := api.NewClient(baseURL)
			client.SetToken(token)
			return client
		},
	}, nil
}

// ConnectionCatalog implements v2.CatalogProvider.
func (p *ConnectionCatalogProvider) ConnectionCatalog(ctx context.Context) (v2.ConnectionCatalog, error) {
	if p == nil || ctx == nil {
		return v2.ConnectionCatalog{}, &controller.Error{Code: controller.ErrorCodeInvalidArgument, Detail: "connection catalog context is required"}
	}
	if err := ctx.Err(); err != nil {
		return v2.ConnectionCatalog{}, err
	}
	token, err := p.tokens(ctx)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return v2.ConnectionCatalog{}, contextErr
		}
		return v2.ConnectionCatalog{}, &controller.Error{Code: controller.ErrorCodeServiceUnavailable, Detail: "authentication token is unavailable"}
	}
	if token == "" || config.IsTokenExpired(token) {
		return v2.ConnectionCatalog{}, reauthRequired()
	}
	response, err := p.newClient(p.baseURL, token).GetAvailableGroupsContext(ctx)
	if err != nil {
		if isAuthenticationError(err) {
			return v2.ConnectionCatalog{}, reauthRequired()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return v2.ConnectionCatalog{}, contextErr
		}
		return v2.ConnectionCatalog{}, &controller.Error{Code: controller.ErrorCodeServiceUnavailable, Detail: "connection catalog is unavailable"}
	}
	if response == nil {
		return v2.ConnectionCatalog{}, &controller.Error{Code: controller.ErrorCodeServiceUnavailable, Detail: "connection catalog is unavailable"}
	}
	exitNodes, err := normalizeAPIExitNodes(response.ExitNodes)
	if err != nil {
		return v2.ConnectionCatalog{}, &controller.Error{Code: controller.ErrorCodeServiceUnavailable, Detail: "connection catalog contains conflicting exit nodes"}
	}

	catalog := v2.ConnectionCatalog{
		ConfigurationID:      p.configurationID,
		Projects:             make([]v2.CatalogProject, 0, len(response.Groups)),
		ExitNodes:            make([]v2.CatalogExitNode, 0, len(exitNodes)),
		HasOverlap:           response.HasOverlap,
		SelectionRecommended: response.SelectionRecommended,
		AllowAll:             response.AllowAll,
		VPNMode:              response.VPNMode,
	}
	for _, group := range response.Groups {
		project := v2.CatalogProject{
			GroupID:         group.ID,
			Name:            group.Name,
			Description:     group.Description,
			OnlineResources: group.OnlinePublishers,
			CIDRs:           append([]string(nil), group.CIDRs...),
			Resources:       make([]v2.CatalogResource, 0, len(group.Publishers)),
		}
		for _, publisher := range group.Publishers {
			project.Resources = append(project.Resources, v2.CatalogResource{
				PublisherID:  publisher.ID,
				Name:         publisher.Name,
				Status:       publisher.Status,
				ExposedCIDRs: append([]string(nil), publisher.ExposedCIDRs...),
			})
		}
		catalog.Projects = append(catalog.Projects, project)
	}
	for _, exitNode := range exitNodes {
		catalog.ExitNodes = append(catalog.ExitNodes, v2.CatalogExitNode{
			ExitNodeID: exitNode.ID,
			Name:       exitNode.Name,
			Location:   exitNode.Location,
			Status:     exitNode.Status,
		})
	}
	return catalog, nil
}

func normalizeAPIExitNodes(nodes []api.ExitNodeInfo) ([]api.ExitNodeInfo, error) {
	byID := make(map[string]api.ExitNodeInfo, len(nodes))
	normalized := make([]api.ExitNodeInfo, 0, len(nodes))
	for _, node := range nodes {
		if existing, found := byID[node.ID]; found {
			if !sameAPIExitNode(existing, node) {
				return nil, errors.New("duplicate exit node has conflicting metadata")
			}
			continue
		}
		byID[node.ID] = node
		normalized = append(normalized, node)
	}
	return normalized, nil
}

func sameAPIExitNode(left, right api.ExitNodeInfo) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Location != right.Location || left.Status != right.Status {
		return false
	}
	if left.PublisherIndex == nil || right.PublisherIndex == nil {
		return left.PublisherIndex == nil && right.PublisherIndex == nil
	}
	return *left.PublisherIndex == *right.PublisherIndex
}
