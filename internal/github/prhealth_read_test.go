package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redtidev1918/releasegraph/internal/domain"
)

func TestReadRateLimitAndTimeout(t *testing.T) {
	for _, status := range []int{429, 403, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			if err := NewForTest(server.URL).Get(context.Background(), "x", nil); err != nil || calls != 2 {
				t.Fatalf("calls %d err %v", calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewForTest("http://127.0.0.1:1").Get(ctx, "x", nil); err == nil {
		t.Fatal("timeout treated as success")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if got := retryDelay(http.Header{"Retry-After": []string{now.Add(20 * time.Second).Format(http.TimeFormat)}}, time.Second, now); got != 20*time.Second {
		t.Fatal(got)
	}
}
func TestCollectionRejectsPartialAndLoops(t *testing.T) {
	for _, kind := range []string{"partial", "loop", "external", "null"} {
		t.Run(kind, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "loop" {
					w.Header().Set("Link", `<`+server.URL+`/repos/o/r/actions/runs?per_page=100>; rel="next"`)
				}
				if kind == "external" {
					w.Header().Set("Link", `<https://example.com/steal>; rel="next"`)
				}
				if kind == "null" {
					fmt.Fprint(w, `null`)
					return
				}
				fmt.Fprint(w, `{"total_count":2,"workflow_runs":[{"id":1}]}`)
			}))
			defer server.Close()
			c := NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})
			if _, err := c.Collection(context.Background(), "repos/o/r/actions/runs", "workflow_runs"); err == nil {
				t.Fatal("partial response passed")
			}
		})
	}
}
