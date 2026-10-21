// Package ghapi polls the GitHub REST API for Actions workflow runs.
package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://api.github.com"
	// maxParallel caps how many repos are in flight at once, so a long watch
	// list does not open a connection per repo.
	maxParallel = 4
	// maxErrBody is how much of a failed response is read for its message.
	maxErrBody = 4 << 10
	// repoPageSize is the largest page the repos endpoints will serve.
	repoPageSize = 100
	// maxRepoPages bounds a wildcard expansion over a very large owner.
	maxRepoPages = 10
)

// errNotFound distinguishes "this owner is not an organisation" from a real
// failure, so a wildcard can try the user endpoint instead.
var errNotFound = errors.New("not found")

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
//
// Every repo's ETag is remembered so the next poll can be conditional. GitHub
// answers an unchanged repo with 304 Not Modified and does not charge it
// against the rate limit, which is what lets a short poll interval stay
// affordable across a long watch list.
type Client struct {
	hc      *http.Client
	base    string // API root; overridden in tests
	token   string
	repos   []string
	perRepo int

	mu     sync.Mutex
	cached map[string]cacheEntry
}

// cacheEntry is the last good response for one repo.
type cacheEntry struct {
	etag string
	runs []Run
}

// NewClient returns a Client polling the given repos, keeping perRepo runs per repo.
func NewClient(token string, repos []string, perRepo int) *Client {
	if perRepo < 1 {
		perRepo = 5
	}
	return &Client{
		hc:      &http.Client{Timeout: 20 * time.Second},
		base:    apiBase,
		token:   token,
		repos:   repos,
		perRepo: perRepo,
		cached:  make(map[string]cacheEntry, len(repos)),
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
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, repo := range c.repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i] = outcome{err: fmt.Errorf("%s: %w", repo, ctx.Err())}
				return
			}
			runs, rl, err := c.fetchRepo(ctx, repo)
			out[i] = outcome{runs, rl, err}
		}()
	}
	wg.Wait()

	total, errs := 0, 0
	for _, o := range out {
		total += len(o.runs)
		if o.err != nil {
			errs++
		}
	}
	// Copy into a fresh slice: callers sort and trim Runs in place, and some
	// of these are the cached slices behind a 304.
	res := Result{Runs: make([]Run, 0, total), RateLimit: RateLimit{Remaining: -1}}
	joined := make([]error, 0, errs)
	for _, o := range out {
		res.Runs = append(res.Runs, o.runs...)
		if o.err != nil {
			joined = append(joined, o.err)
		}
		// The tightest budget seen is the one worth reporting.
		if o.rl.Remaining >= 0 &&
			(res.RateLimit.Remaining < 0 || o.rl.Remaining < res.RateLimit.Remaining) {
			res.RateLimit = o.rl
		}
	}
	res.Err = errors.Join(joined...)
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
	prev := c.cacheGet(repo)

	// exclude_pull_requests drops a list the board never reads, which is most
	// of the payload on a busy repo.
	url := fmt.Sprintf("%s/repos/%s/actions/runs?per_page=%d&exclude_pull_requests=true",
		c.base, repo, c.perRepo)
	req, err := c.newRequest(ctx, http.MethodGet, url)
	if err != nil {
		return nil, rl, fmt.Errorf("%s: %w", repo, err)
	}
	if prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, rl, fmt.Errorf("%s: %w", repo, err)
	}
	defer resp.Body.Close()
	rl = parseRateLimit(resp.Header)

	switch resp.StatusCode {
	case http.StatusNotModified:
		return prev.runs, rl, nil
	case http.StatusOK:
	default:
		return nil, rl, fmt.Errorf("%s: %s%s", repo, resp.Status, apiMessage(resp.Body))
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
	c.cachePut(repo, cacheEntry{etag: resp.Header.Get("ETag"), runs: runs})
	return runs, rl, nil
}

// newRequest builds a request carrying clack's headers and credentials.
func (c *Client) newRequest(ctx context.Context, method, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "clack")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// Rerun asks GitHub to run a completed workflow run again. The run keeps
// its ID and goes back to queued, so the board shows the change on its next
// poll rather than growing a second row.
//
// This is the only thing clack writes, and a token from `gh auth token`
// may well not be allowed to, so a refusal says what is missing.
func (c *Client) Rerun(ctx context.Context, repo string, runID int64) error {
	url := fmt.Sprintf("%s/repos/%s/actions/runs/%d/rerun", c.base, repo, runID)
	req, err := c.newRequest(ctx, http.MethodPost, url)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("re-run %s: %w", repo, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("re-run %s: %s%s (the token needs the actions:write scope)",
			repo, resp.Status, apiMessage(resp.Body))
	default:
		return fmt.Errorf("re-run %s: %s%s", repo, resp.Status, apiMessage(resp.Body))
	}
}

// Repos is the list the client is polling.
func (c *Client) Repos() []string { return c.repos }

// ExpandWildcards replaces every OWNER/* entry with that owner's
// repositories, most recently pushed first, keeping at most limit of them.
//
// The cap is not politeness, it is arithmetic: the board spends one request
// per repo per poll, so an owner with two hundred repositories would burn
// the hourly budget in minutes. Sorting by last push means the cap keeps
// the repositories somebody is actually working in.
func (c *Client) ExpandWildcards(ctx context.Context, limit int) error {
	if limit < 1 {
		limit = 1
	}
	out := make([]string, 0, len(c.repos))
	seen := make(map[string]bool, len(c.repos))
	add := func(repo string) {
		if key := strings.ToLower(repo); !seen[key] {
			seen[key] = true
			out = append(out, repo)
		}
	}
	for _, repo := range c.repos {
		owner, name, _ := strings.Cut(repo, "/")
		if name != "*" {
			add(repo)
			continue
		}
		found, err := c.ownerRepos(ctx, owner, limit)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return fmt.Errorf("%s: no repositories to watch", repo)
		}
		for _, r := range found {
			add(r)
		}
	}
	c.repos = out
	return nil
}

// ownerRepos lists an owner's repositories. An owner is either an
// organisation or a user, and only one of those endpoints will answer.
func (c *Client) ownerRepos(ctx context.Context, owner string, limit int) ([]string, error) {
	var err error
	for _, kind := range [...]string{"orgs", "users"} {
		var repos []string
		repos, err = c.listRepos(ctx, fmt.Sprintf("%s/%s/%s/repos", c.base, kind, owner), limit)
		if err == nil {
			return repos, nil
		}
		if !errors.Is(err, errNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s/*: no such organisation or user", owner)
}

type repoInfo struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
}

// listRepos pages through a repos endpoint, newest push first.
func (c *Client) listRepos(ctx context.Context, endpoint string, limit int) ([]string, error) {
	var out []string
	for page := 1; page <= maxRepoPages && len(out) < limit; page++ {
		url := fmt.Sprintf("%s?sort=pushed&direction=desc&per_page=%d&page=%d",
			endpoint, repoPageSize, page)
		req, err := c.newRequest(ctx, http.MethodGet, url)
		if err != nil {
			return nil, err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}
		batch, err := decodeRepos(resp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, r := range batch {
			// An archived repository cannot produce another run.
			if r.Archived || r.FullName == "" {
				continue
			}
			if out = append(out, r.FullName); len(out) == limit {
				return out, nil
			}
		}
		if len(batch) < repoPageSize {
			break
		}
	}
	return out, nil
}

func decodeRepos(resp *http.Response) ([]repoInfo, error) {
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNotFound
	default:
		return nil, fmt.Errorf("%s%s", resp.Status, apiMessage(resp.Body))
	}
	var batch []repoInfo
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// cacheGet returns the last good response for repo. The runs it carries are
// shared with the cache, so callers must copy before sorting or mutating.
func (c *Client) cacheGet(repo string) cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached[repo]
}

func (c *Client) cachePut(repo string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.etag == "" {
		delete(c.cached, repo) // nothing to revalidate against next time
		return
	}
	c.cached[repo] = e
}

// apiMessage pulls GitHub's own explanation out of a failed response, which
// says far more than the status line does ("Bad credentials", "Not Found").
func apiMessage(body io.Reader) string {
	var msg struct {
		Message string `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(body, maxErrBody)).Decode(&msg) != nil || msg.Message == "" {
		return ""
	}
	return ": " + msg.Message
}

func parseRateLimit(h http.Header) RateLimit {
	rl := RateLimit{Remaining: -1}
	if n, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil {
		rl.Remaining = n
	}
	if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		rl.Reset = time.Unix(n, 0)
	}
	return rl
}
