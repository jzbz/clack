package ghapi

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// liveClient returns a Client authenticated the way clack itself is, or
// skips. Opt-in: CLACK_LIVE=1.
func liveClient(t *testing.T, repos ...string) *Client {
	t.Helper()
	if os.Getenv("CLACK_LIVE") == "" {
		t.Skip("set CLACK_LIVE=1 to hit the real GitHub API")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			t.Skip("no GITHUB_TOKEN and gh auth token failed:", err)
		}
		token = strings.TrimSpace(string(out))
	}
	return NewClient(token, repos, 3)
}

func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestLiveFetch hits the real GitHub API once and checks the board gets a
// usable run out of it.
func TestLiveFetch(t *testing.T) {
	res := liveClient(t, "cli/cli").Fetch(liveContext(t))
	if res.Err != nil {
		t.Fatalf("fetch: %v", res.Err)
	}
	if len(res.Runs) == 0 {
		t.Fatal("no runs returned")
	}
	t.Logf("rate limit: remaining=%d reset=%v", res.RateLimit.Remaining, res.RateLimit.Reset)
	for _, r := range res.Runs {
		t.Logf("%d %s %s %s %s/%s started=%s", r.ID, r.Repo, r.Workflow, r.Branch, r.Status, r.Conclusion, r.StartedAt)
		if r.ID == 0 || r.Repo == "" || r.Workflow == "" || r.Status == "" || r.StartedAt.IsZero() {
			t.Errorf("incomplete run: %+v", r)
		}
	}
}

// TestLiveConditionalRequest is the one that matters for the rate limit: a
// second poll of an unchanged repo must be revalidated with its ETag and
// come back 304, which GitHub does not charge. The cache lives on the
// Client, so this has to be two fetches on the same one — a second run of
// the test binary starts with an empty cache and would prove nothing.
func TestLiveConditionalRequest(t *testing.T) {
	c := liveClient(t, "cli/cli")
	ctx := liveContext(t)

	first := c.Fetch(ctx)
	if first.Err != nil {
		t.Fatalf("first fetch: %v", first.Err)
	}
	if first.RateLimit.Remaining < 0 {
		t.Skip("no rate-limit headers to compare against")
	}

	second := c.Fetch(ctx)
	if second.Err != nil {
		t.Fatalf("second fetch: %v", second.Err)
	}
	t.Logf("remaining: %d then %d", first.RateLimit.Remaining, second.RateLimit.Remaining)

	// cli/cli is busy enough that a run really can land between the two
	// polls. That is a 200 and a charged request, and says nothing either
	// way, so only judge the budget when nothing moved.
	if !sameRuns(first.Runs, second.Runs) {
		t.Skip("the repo changed between polls; nothing to conclude")
	}
	if second.RateLimit.Remaining < first.RateLimit.Remaining {
		t.Errorf("an unchanged repo cost %d request(s) of quota: the conditional request did not take",
			first.RateLimit.Remaining-second.RateLimit.Remaining)
	}
}

func sameRuns(a, b []Run) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || !a[i].UpdatedAt.Equal(b[i].UpdatedAt) || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}
