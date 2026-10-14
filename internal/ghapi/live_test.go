package ghapi

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveFetch hits the real GitHub API once. Opt-in: CLACK_LIVE=1.
func TestLiveFetch(t *testing.T) {
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

	c := NewClient(token, []string{"cli/cli"}, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := c.Fetch(ctx)
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
