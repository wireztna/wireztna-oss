//go:build darwin

package darwin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type catalogAPIFake struct {
	response *api.AvailableGroupsResponse
	err      error
	calls    int
}

func (f *catalogAPIFake) GetAvailableGroupsContext(context.Context) (*api.AvailableGroupsResponse, error) {
	f.calls++
	return f.response, f.err
}

func TestConnectionCatalogProviderUsesCurrentTokenAndPublicIdentityOnly(t *testing.T) {
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	token := catalogTestJWT(t, time.Now().Add(time.Hour))
	provider, err := NewConnectionCatalogProvider(&config.ClientConfig{
		APIURL: "https://control.example", PrivateKey: privateKey.String(),
	}, func(context.Context) (string, error) {
		return token, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &catalogAPIFake{response: &api.AvailableGroupsResponse{
		Groups: []api.GroupInfo{{
			ID: "group-a", Name: "Project A", Description: "project", OnlinePublishers: 1, CIDRs: []string{"10.1.0.0/16"},
			Publishers: []api.PublisherInfo{{ID: "publisher-a", Name: "Resource A", Status: "online", ExposedCIDRs: []string{"10.1.2.0/24"}}},
		}},
		ExitNodes:            []api.ExitNodeInfo{{ID: "exit-a", Name: "Exit A", Location: "us", Status: "online"}},
		HasOverlap:           true,
		SelectionRecommended: true,
		AllowAll:             true,
		VPNMode:              true,
	}}
	provider.newClient = func(baseURL, receivedToken string) availableGroupsAPI {
		if baseURL != "https://control.example" || receivedToken != token {
			t.Fatalf("API client inputs = %q, %q", baseURL, receivedToken)
		}
		return fake
	}

	catalog, err := provider.ConnectionCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	publicIdentity := privateKey.PublicKey().String()
	digest := sha256.Sum256([]byte(publicIdentity))
	wantConfigurationID := "wg-public-sha256:" + hex.EncodeToString(digest[:])
	if catalog.ConfigurationID != wantConfigurationID {
		t.Fatalf("configuration_id = %q, want %q", catalog.ConfigurationID, wantConfigurationID)
	}
	if len(catalog.Projects) != 1 || catalog.Projects[0].GroupID != "group-a" || len(catalog.Projects[0].Resources) != 1 || catalog.Projects[0].Resources[0].PublisherID != "publisher-a" {
		t.Fatalf("projects = %#v", catalog.Projects)
	}
	if len(catalog.ExitNodes) != 1 || catalog.ExitNodes[0].ExitNodeID != "exit-a" || !catalog.HasOverlap || !catalog.SelectionRecommended || !catalog.AllowAll || !catalog.VPNMode {
		t.Fatalf("catalog policy/exit nodes = %#v", catalog)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, secretOrRawIdentity := range []string{privateKey.String(), publicIdentity, token} {
		if strings.Contains(string(encoded), secretOrRawIdentity) {
			t.Fatalf("catalog exposed credential or raw identity %q", secretOrRawIdentity)
		}
	}
}

func TestConnectionCatalogProviderRequiresFreshAuthenticationWithoutCallingAPI(t *testing.T) {
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"missing": "",
		"expired": catalogTestJWT(t, time.Now().Add(-time.Hour)),
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := NewConnectionCatalogProvider(&config.ClientConfig{
				APIURL: "https://control.example", PrivateKey: privateKey.String(),
			}, func(context.Context) (string, error) { return token, nil })
			if err != nil {
				t.Fatal(err)
			}
			fake := &catalogAPIFake{}
			provider.newClient = func(string, string) availableGroupsAPI { return fake }
			_, err = provider.ConnectionCatalog(context.Background())
			var structured *controller.Error
			if !errors.As(err, &structured) || structured.Code != controller.ErrorCodeReauthRequired {
				t.Fatalf("ConnectionCatalog() error = %v", err)
			}
			if fake.calls != 0 {
				t.Fatalf("API calls = %d, want 0", fake.calls)
			}
		})
	}
}

func catalogTestJWT(t *testing.T, expires time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": expires.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("e30.%s.signature", base64.RawURLEncoding.EncodeToString(payload))
}

func TestNormalizeAPIExitNodesCoalescesIdenticalAndPreservesServerOrder(t *testing.T) {
	nodes, err := normalizeAPIExitNodes([]api.ExitNodeInfo{
		{ID: "exit-c", Name: "C", Status: "online", PublisherIndex: catalogPublisherIndex(9)},
		{ID: "exit-b", Name: "B", Status: "online", PublisherIndex: catalogPublisherIndex(2)},
		{ID: "exit-a", Name: "A", Status: "online", PublisherIndex: nil},
		{ID: "exit-b", Name: "B", Status: "online", PublisherIndex: catalogPublisherIndex(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 || nodes[0].ID != "exit-c" || nodes[1].ID != "exit-b" || nodes[2].ID != "exit-a" {
		t.Fatalf("normalized exit nodes = %#v", nodes)
	}
}

func catalogPublisherIndex(value int) *int {
	return &value
}

func TestConnectionCatalogProviderRejectsConflictingDuplicateExitNodes(t *testing.T) {
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewConnectionCatalogProvider(&config.ClientConfig{
		APIURL: "https://control.example", PrivateKey: privateKey.String(),
	}, func(context.Context) (string, error) {
		return catalogTestJWT(t, time.Now().Add(time.Hour)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &catalogAPIFake{response: &api.AvailableGroupsResponse{ExitNodes: []api.ExitNodeInfo{
		{ID: "exit-a", Name: "Primary", Status: "online", PublisherIndex: catalogPublisherIndex(1)},
		{ID: "exit-a", Name: "Conflicting", Status: "online", PublisherIndex: catalogPublisherIndex(1)},
	}}}
	provider.newClient = func(string, string) availableGroupsAPI { return fake }

	_, err = provider.ConnectionCatalog(context.Background())
	structured, ok := controller.AsError(err)
	if !ok || structured.Code != controller.ErrorCodeServiceUnavailable ||
		structured.Detail != "connection catalog contains conflicting exit nodes" {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
}
