package ghapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const runsJSON = `{"workflow_runs":[
  {"id":11,"name":"ci","head_branch":"main","status":"completed","conclusion":"success",
   "html_url":"https://github.com/acme/web/actions/runs/11",
   "created_at":"2026-10-18T10:00:00Z","updated_at":"2026-10-18T10:05:00Z",
   "run_started_at":"2026-10-18T10:01:00Z","actor":{"login":"octocat"}},
  {"id":12,"name":"lint","head_branch":"fix/css","status":"in_progress","conclusion":null,
   "html_url":"https://github.com/acme/web/actions/runs/12",
   "created_at":"2026-10-18T10:02:00Z","updated_at":"2026-10-18T10:02:00Z",
   "actor":{"login":"mona"}}
]}`

// newTestClient points a Client at srv instead of api.github.com.
func newTestClient(t *testing.T, srv *httptest.Server, repos ...string) *Client {
	t.Helper()
	c := NewClient("tok", repos, 5)
	c.base = srv.URL
	c.hc = srv.Client()
	return c
}

func TestFetchParsesRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.Contains(r.URL.RawQuery, "exclude_pull_requests=true") {
			t.Errorf("query %q does not trim the payload", r.URL.RawQuery)
		}
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Reset", "1800000000")
		fmt.Fprint(w, runsJSON)
	}))
	defer srv.Close()

	res := newTestClient(t, srv, "acme/web").Fetch(t.Context())
	if res.Err != nil {
		t.Fatalf("fetch: %v", res.Err)
	}
	if len(res.Runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(res.Runs))
	}
	got := res.Runs[0]
	want := Run{
		ID: 11, Repo: "acme/web", Workflow: "ci", Branch: "main", Actor: "octocat",
		Status: "completed", Conclusion: "success",
		URL:       "https://github.com/acme/web/actions/runs/11",
		StartedAt: time.Date(2026, 10, 18, 10, 1, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 18, 10, 5, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("run 0:\n got %+v\nwant %+v", got, want)
	}
	// Without run_started_at the board falls back to created_at.
	if w := time.Date(2026, 10, 18, 10, 2, 0, 0, time.UTC); !res.Runs[1].StartedAt.Equal(w) {
		t.Errorf("run 1 StartedAt = %v, want %v", res.Runs[1].StartedAt, w)
	}
	if res.RateLimit.Remaining != 4321 || res.RateLimit.Reset.Unix() != 1800000000 {
		t.Errorf("rate limit = %+v", res.RateLimit)
	}
}

// A repo that has not changed is revalidated with its ETag. GitHub answers
// 304 and charges no quota, which is the point of keeping the cache.
func TestFetchRevalidatesWithETag(t *testing.T) {
	var hits, conditional atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("ETag", `W/"abc123"`)
		w.Header().Set("X-RateLimit-Remaining", "100")
		if r.Header.Get("If-None-Match") == `W/"abc123"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, runsJSON)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "acme/web")
	first := c.Fetch(t.Context())
	if first.Err != nil || len(first.Runs) != 2 {
		t.Fatalf("first poll: %d runs, err %v", len(first.Runs), first.Err)
	}

	second := c.Fetch(t.Context())
	if second.Err != nil {
		t.Fatalf("second poll: %v", second.Err)
	}
	if conditional.Load() != 1 {
		t.Errorf("second poll was not conditional (%d hits)", hits.Load())
	}
	if len(second.Runs) != 2 || second.Runs[0].ID != 11 {
		t.Fatalf("304 did not serve the cached runs: %+v", second.Runs)
	}
	if second.RateLimit.Remaining != 100 {
		t.Errorf("rate limit lost on 304: %+v", second.RateLimit)
	}

	// The board sorts and trims what it is handed, so a 304 must not give
	// out the cache's own backing array.
	second.Runs[0] = Run{ID: -1}
	third := c.Fetch(t.Context())
	if third.Runs[0].ID != 11 {
		t.Fatalf("cache was corrupted by its caller: %+v", third.Runs)
	}
}

// One bad repo must not cost the others their runs.
func TestFetchPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/acme/web/"):
			w.Header().Set("X-RateLimit-Remaining", "500")
			fmt.Fprint(w, runsJSON)
		default:
			w.Header().Set("X-RateLimit-Remaining", "12")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	defer srv.Close()

	res := newTestClient(t, srv, "acme/web", "acme/ghost").Fetch(t.Context())
	if len(res.Runs) != 2 {
		t.Errorf("got %d runs, want the 2 from the healthy repo", len(res.Runs))
	}
	if res.Err == nil {
		t.Fatal("the failing repo was not reported")
	}
	if msg := res.Err.Error(); !strings.Contains(msg, "acme/ghost") || !strings.Contains(msg, "Not Found") {
		t.Errorf("error %q does not say which repo failed or why", msg)
	}
	// The tightest budget seen is the one worth acting on.
	if res.RateLimit.Remaining != 12 {
		t.Errorf("rate limit = %d, want the lowest seen (12)", res.RateLimit.Remaining)
	}
}

func TestFetchNoRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"workflow_runs":[]}`)
	}))
	defer srv.Close()

	res := newTestClient(t, srv, "acme/web").Fetch(t.Context())
	if res.Err != nil {
		t.Fatalf("fetch: %v", res.Err)
	}
	if res.RateLimit.Remaining != -1 {
		t.Errorf("unknown budget reported as %d, want -1", res.RateLimit.Remaining)
	}
}

func TestFetchHonoursCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	res := newTestClient(t, srv, "acme/web").Fetch(ctx)
	if res.Err == nil {
		t.Fatal("a cancelled fetch reported success")
	}
}
