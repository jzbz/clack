// Package ghapi polls the GitHub REST API for Actions workflow runs.
package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const apiBase = "https://api.github.com"

// Run is one GitHub Actions workflow run, flattened to what the board shows.
type Run struct {
	ID         int64
	Repo       string // OWNER/NAME
	Workflow   string
	Branch     string
	Actor      string
	Status     string // queued, requested, waiting, pending, in_progress, completed
	Conclusion string // success, failure, cancelled, ... (empty until completed)
	URL        string // html_url of the run
	StartedAt  time.Time
	UpdatedAt  time.Time
}

// RateLimit is the GitHub API rate-limit state observed during a fetch.
type RateLimit struct {
	Remaining int // -1 when unknown
	Reset     time.Time
}

// Result is the outcome of one polling pass over every watched repo.
type Result struct {
	Runs      []Run
	RateLimit RateLimit
	// Err aggregates per-repo failures. Runs may still hold partial data.
	Err error
}

// Client polls /repos/{owner}/{repo}/actions/runs for a fixed list of repos.
type Client struct {
	hc      *http.Client
	token   string
	repos   []string
	perRepo int
}

// NewClient returns a Client polling the given repos, keeping perRepo runs per repo.
func NewClient(token string, repos []string, perRepo int) *Client {
	if perRepo < 1 {
		perRepo = 5
	}
	return &Client{
		hc:      &http.Client{Timeout: 20 * time.Second},
		token:   token,
		repos:   repos,
		perRepo: perRepo,
	}
}

// Fetch polls every repo concurrently and merges the results.
func (c *Client) Fetch(ctx context.Context) Result {
	type outcome struct {
		runs []Run
		rl   RateLimit
		err  error
	}
	out := make([]outcome, len(c.repos))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, repo := range c.repos {
		wg.Add(1)
		go func(i int, repo string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runs, rl, err := c.fetchRepo(ctx, repo)
			out[i] = outcome{runs, rl, err}
		}(i, repo)
	}
	wg.Wait()

	res := Result{RateLimit: RateLimit{Remaining: -1}}
	var errs []error
	for _, o := range out {
		res.Runs = append(res.Runs, o.runs...)
		if o.err != nil {
			errs = append(errs, o.err)
		}
		if o.rl.Remaining >= 0 &&
			(res.RateLimit.Remaining < 0 || o.rl.Remaining < res.RateLimit.Remaining) {
			res.RateLimit = o.rl
		}
	}
	res.Err = errors.Join(errs...)
	return res
}

type runsResponse struct {
	WorkflowRuns []struct {
		ID           int64      `json:"id"`
		Name         string     `json:"name"`
		HeadBranch   string     `json:"head_branch"`
		Status       string     `json:"status"`
		Conclusion   string     `json:"conclusion"`
		HTMLURL      string     `json:"html_url"`
		CreatedAt    time.Time  `json:"created_at"`
		UpdatedAt    time.Time  `json:"updated_at"`
		RunStartedAt *time.Time `json:"run_started_at"`
		Actor        struct {
			Login string `json:"login"`
		} `json:"actor"`
	} `json:"workflow_runs"`
}

func (c *Client) fetchRepo(ctx context.Context, repo string) ([]Run, RateLimit, error) {
	rl := RateLimit{Remaining: -1}
	url := fmt.Sprintf("%s/repos/%s/actions/runs?per_page=%d", apiBase, repo, c.perRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, rl, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "clack")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, rl, fmt.Errorf("%s: %w", repo, err)
	}
	defer resp.Body.Close()
	rl = parseRateLimit(resp.Header)

	if resp.StatusCode != http.StatusOK {
		return nil, rl, fmt.Errorf("%s: %s", repo, resp.Status)
	}
	var body runsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, rl, fmt.Errorf("%s: %w", repo, err)
	}

	runs := make([]Run, 0, len(body.WorkflowRuns))
	for _, wr := range body.WorkflowRuns {
		started := wr.CreatedAt
		if wr.RunStartedAt != nil {
			started = *wr.RunStartedAt
		}
		runs = append(runs, Run{
			ID:         wr.ID,
			Repo:       repo,
			Workflow:   wr.Name,
			Branch:     wr.HeadBranch,
			Actor:      wr.Actor.Login,
			Status:     wr.Status,
			Conclusion: wr.Conclusion,
			URL:        wr.HTMLURL,
			StartedAt:  started,
			UpdatedAt:  wr.UpdatedAt,
		})
	}
	return runs, rl, nil
}

func parseRateLimit(h http.Header) RateLimit {
	rl := RateLimit{Remaining: -1}
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rl.Remaining = n
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			rl.Reset = time.Unix(n, 0)
		}
	}
	return rl
}
