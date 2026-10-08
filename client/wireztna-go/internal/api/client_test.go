package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wireztna/client/internal/config"
)

func TestRenewSessionUsesExplicitOwnerLockWithoutHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("explicit UID ownership is a Unix service contract")
	}
	configDir := t.TempDir()
	if err := os.Chmod(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	t.Setenv("SUDO_USER", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"session","preshared_key":"psk"}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, WithRemoteMutationOwner(configDir, uint32(os.Getuid())))
	if _, err := client.RenewSessionContext(context.Background(), "group"); err != nil {
		t.Fatalf("RenewSessionContext() with explicit owner lock error = %v", err)
	}
	info, err := os.Lstat(filepath.Join(configDir, "remote-mutation.lock"))
	if err != nil {
		t.Fatalf("inspect explicit lock: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("explicit lock mode = %v, want regular 0600", info.Mode())
	}
}

func TestRenewSessionSerializesRemoteMutationsAcrossClients(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	requestCount := 0
	firstEntered := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		if requestCount == 1 {
			close(firstEntered)
		}
		mu.Unlock()

		<-release

		mu.Lock()
		inFlight--
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"session","preshared_key":"psk"}`)
	}))
	defer server.Close()

	client1 := NewClient(server.URL)
	client2 := NewClient(server.URL)
	results := make(chan error, 2)
	go func() {
		_, err := client1.RenewSessionContext(context.Background(), "")
		results <- err
	}()

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first remote mutation did not start")
	}

	go func() {
		_, err := client2.RenewSessionContext(context.Background(), "")
		results <- err
	}()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	gotRequests := requestCount
	gotMax := maxInFlight
	mu.Unlock()
	if gotRequests != 1 || gotMax != 1 {
		close(release)
		t.Fatalf("while first mutation blocked: requests=%d max in-flight=%d, want 1/1", gotRequests, gotMax)
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("renew session: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serialized renewal did not complete")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if requestCount != 2 || maxInFlight != 1 {
		t.Fatalf("requests=%d max in-flight=%d, want 2/1", requestCount, maxInFlight)
	}
}

func TestRenewSessionLockWaitHonorsContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")

	firstEntered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-firstEntered:
		default:
			close(firstEntered)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"session","preshared_key":"psk"}`)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	firstResult := make(chan error, 1)
	go func() {
		_, err := client.RenewSessionContext(context.Background(), "")
		firstResult <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first remote mutation did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.RenewSessionContext(ctx, ""); err == nil || ctx.Err() == nil {
		close(release)
		t.Fatalf("lock wait error = %v, context error = %v", err, ctx.Err())
	}

	close(release)
	select {
	case err := <-firstResult:
		if err != nil {
			t.Fatalf("first renewal: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first renewal did not complete")
	}
}

func TestQueuedStaleRenewalDoesNotReachServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}

	var mu sync.Mutex
	requestCount := 0
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		if requestCount == 1 {
			close(firstEntered)
		}
		mu.Unlock()
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"session","preshared_key":"psk"}`)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	firstResult := make(chan error, 1)
	go func() {
		_, err := client.RenewSessionContext(context.Background(), "")
		firstResult <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first renewal did not start")
	}

	staleResult := make(chan error, 1)
	go func() {
		ctx := WithLifecycleGeneration(context.Background(), generation)
		_, err := client.RenewSessionContext(ctx, "")
		staleResult <- err
	}()
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("invalidate lifecycle: %v", err)
	}
	close(release)

	if err := <-firstResult; err != nil {
		t.Fatalf("first renewal: %v", err)
	}
	if err := <-staleResult; !errors.Is(err, config.ErrStaleLifecycle) {
		t.Fatalf("queued renewal error = %v, want stale lifecycle", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("server requests = %d, want 1", requestCount)
	}
}

func TestSameEffectiveOriginNormalizesDefaultPorts(t *testing.T) {
	parse := func(value string) *url.URL {
		parsed, err := url.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	if !sameEffectiveOrigin(parse("https://EXAMPLE.com:443/next"), parse("https://example.com/api")) {
		t.Fatal("equivalent HTTPS origins were rejected")
	}
	if !sameEffectiveOrigin(parse("http://example.com/next"), parse("http://example.com:80/api")) {
		t.Fatal("equivalent HTTP origins were rejected")
	}
	for _, candidate := range []string{"http://example.com", "https://example.com:444", "https://other.example"} {
		if sameEffectiveOrigin(parse(candidate), parse("https://example.com")) {
			t.Fatalf("unsafe origin %q was accepted", candidate)
		}
	}
}

func TestClientBlocksBearerRedirectOutsideConfiguredOrigin(t *testing.T) {
	var destinationRequests int
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationRequests++
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewClient(source.URL)
	client.SetToken("sensitive-token")
	if _, err := client.GetAvailableGroupsContext(context.Background()); err == nil {
		t.Fatal("cross-origin redirect was accepted")
	}
	if destinationRequests != 0 {
		t.Fatalf("redirect destination received %d request(s), want 0", destinationRequests)
	}
}

func TestAvailableGroupsPreservesNullablePublisherIndexAndServerOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions/available-groups" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"groups":[],"has_overlap":false,"overlap_details":[],"selection_recommended":false,"allow_all":true,"exit_nodes":[{"id":"exit-1","name":"One","location":"","status":"online","publisher_index":1},{"id":"exit-2","name":"Two","location":"","status":"online","publisher_index":2},{"id":"exit-null","name":"Legacy","location":"","status":"online","publisher_index":null}],"vpn_mode":true}`)
	}))
	defer server.Close()

	response, err := NewClient(server.URL).GetAvailableGroupsContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ExitNodes) != 3 || response.ExitNodes[0].ID != "exit-1" || response.ExitNodes[1].ID != "exit-2" || response.ExitNodes[2].ID != "exit-null" {
		t.Fatalf("exit node order = %#v", response.ExitNodes)
	}
	if response.ExitNodes[0].PublisherIndex == nil || *response.ExitNodes[0].PublisherIndex != 1 ||
		response.ExitNodes[1].PublisherIndex == nil || *response.ExitNodes[1].PublisherIndex != 2 ||
		response.ExitNodes[2].PublisherIndex != nil {
		t.Fatalf("publisher indexes = %#v", response.ExitNodes)
	}
}

func TestRenewSessionDistinguishesAuthenticationFromPolicyRejection(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		wantTokenError bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantTokenError: true},
		{name: "forbidden policy", status: http.StatusForbidden, wantTokenError: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("SUDO_USER", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "policy response", test.status)
			}))
			defer server.Close()

			_, err := NewClient(server.URL).RenewSessionContext(context.Background(), "group-a", "exit-a")
			if err == nil {
				t.Fatal("renewal unexpectedly succeeded")
			}
			if got := strings.Contains(err.Error(), "token rejected"); got != test.wantTokenError {
				t.Fatalf("token classification = %v, want %v: %v", got, test.wantTokenError, err)
			}
			if !test.wantTokenError && !strings.Contains(err.Error(), "HTTP 403") {
				t.Fatalf("policy status was not preserved: %v", err)
			}
		})
	}
}
