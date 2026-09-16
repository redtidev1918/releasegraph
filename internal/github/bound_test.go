package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/other/releases/latest" || r.URL.Path == "/repos/acme/app/releases/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return NewForTest(server.URL)
}

// Acceptance: repository scope cannot read another repository.
func TestRepositoryScopeCannotTouchAnotherRepository(t *testing.T) {
	client := testClient(t).Bind(domain.ExecutionContext{
		Scope: domain.ScopeRepository, Repository: "acme/app", CredentialClass: domain.CredentialRepository,
	})
	var target map[string]any
	err := client.Get(context.Background(), "repos/acme/other/releases/latest", &target)
	if !rgerrors.IsKind(err, rgerrors.ScopeViolation) {
		t.Fatalf("cross-repository read = %v, want SCOPE_VIOLATION", err)
	}

	err = client.Get(context.Background(), "repos/acme/other/releases/tags/v1.0.0", &target)
	if !rgerrors.IsKind(err, rgerrors.ScopeViolation) {
		t.Fatalf("cross-repository tag read = %v, want SCOPE_VIOLATION", err)
	}
}

// Acceptance: repository scope may operate on its own repository.
func TestRepositoryScopeAllowsItsOwnRepository(t *testing.T) {
	client := testClient(t).Bind(domain.ExecutionContext{
		Scope: domain.ScopeRepository, Repository: "acme/app", CredentialClass: domain.CredentialRepository,
	})
	var target map[string]any
	if err := client.Get(context.Background(), "repos/acme/app/releases/latest", &target); err != nil {
		t.Fatalf("own-repository read failed: %v", err)
	}
	if target["tag_name"] != "v1.0.0" {
		t.Fatalf("unexpected payload: %v", target)
	}
	if _, err := client.MergedPullRequests(context.Background(), "acme/app"); err != nil {
		t.Fatalf("own-repository PR list failed: %v", err)
	}
}

// Acceptance: dispatching another repository's workflow is a control plane
// capability, never a repository-scope one.
func TestRepositoryScopeCannotDispatch(t *testing.T) {
	client := testClient(t).Bind(domain.ExecutionContext{
		Scope: domain.ScopeRepository, Repository: "acme/app", CredentialClass: domain.CredentialRepository,
	})
	err := client.DispatchWorkflow(context.Background(), "acme/other", "release.yml", "main", map[string]string{"version": "1.0.0"})
	if !rgerrors.IsKind(err, rgerrors.ScopeViolation) {
		t.Fatalf("dispatch from repository scope = %v, want SCOPE_VIOLATION", err)
	}
}

// Fleet scope may reach across repositories, and still obeys its own binding.
func TestFleetScopeMayDispatch(t *testing.T) {
	dispatched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dispatched = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet, CredentialClass: domain.CredentialFleet})

	if err := client.DispatchWorkflow(context.Background(), "acme/other", "release.yml", "main", map[string]string{"repair": "true"}); err != nil {
		t.Fatalf("fleet dispatch failed: %v", err)
	}
	if !dispatched {
		t.Fatal("fleet dispatch did not reach the API")
	}
}

// A path that does not address a single repository is fleet-only.
func TestNonRepositoryPathIsFleetOnly(t *testing.T) {
	repoScoped := testClient(t).Bind(domain.ExecutionContext{Scope: domain.ScopeRepository, Repository: "acme/app"})
	var target []map[string]any
	if err := repoScoped.Get(context.Background(), "user/repos", &target); !rgerrors.IsKind(err, rgerrors.ScopeViolation) {
		t.Fatalf("user/repos from repository scope = %v, want SCOPE_VIOLATION", err)
	}
}
