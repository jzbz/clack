package board

import (
	"context"
	"fmt"
	"time"

	"github.com/jzbz/clack/internal/ghapi"
)

// Demo returns a Fetcher that simulates a busy board without network access.
// Runs advance through scripted status transitions as polls happen.
func Demo() Fetcher {
	type transition struct {
		atPoll int
		status string
		concl  string
	}
	type demoRun struct {
		run  ghapi.Run
		plan []transition
	}

	start := time.Now()
	mk := func(id int64, repo, wf, branch, actor, status, concl string, ago time.Duration, plan []transition) demoRun {
		r := ghapi.Run{
			ID:         id,
			Repo:       repo,
			Workflow:   wf,
			Branch:     branch,
			Actor:      actor,
			Status:     status,
			Conclusion: concl,
			URL:        fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, id),
			StartedAt:  start.Add(-ago),
			UpdatedAt:  start.Add(-ago),
		}
		return demoRun{run: r, plan: plan}
	}

	runs := []demoRun{
		mk(1001, "acme/web", "ci", "main", "octocat", "in_progress", "", 4*time.Minute,
			[]transition{{5, "completed", "success"}}),
		mk(1002, "acme/api", "deploy", "release/v2", "hubot", "in_progress", "", 9*time.Minute,
			[]transition{{8, "completed", "failure"}}),
		mk(1003, "acme/mobile", "tests", "feat/tabs", "mona", "queued", "", 2*time.Minute,
			[]transition{{3, "in_progress", ""}, {9, "completed", "success"}}),
		mk(1007, "acme/mobile", "release", "main", "hubot", "queued", "", time.Minute,
			[]transition{{6, "in_progress", ""}, {12, "completed", "cancelled"}}),
		mk(1004, "acme/infra", "terraform", "main", "octocat", "completed", "success", 14*time.Minute, nil),
		mk(1005, "acme/web", "lint", "fix/css", "mona", "completed", "failure", 26*time.Minute, nil),
		mk(1006, "acme/api", "ci", "main", "octocat", "completed", "success", 41*time.Minute, nil),
		mk(1008, "acme/infra", "backup", "main", "schedule", "completed", "success", 2*time.Hour, nil),
	}
	// Completed runs got their duration from UpdatedAt - StartedAt; give the
	// finished ones a plausible end time.
	for i := range runs {
		if runs[i].run.Status == "completed" {
			runs[i].run.UpdatedAt = runs[i].run.StartedAt.Add(3*time.Minute + time.Duration(i)*time.Minute)
		}
	}

	var poll int
	return func(context.Context) ghapi.Result {
		poll++
		out := make([]ghapi.Run, 0, len(runs))
		for i := range runs {
			r := &runs[i].run
			for _, t := range runs[i].plan {
				if t.atPoll == poll {
					r.Status, r.Conclusion = t.status, t.concl
					if t.status == "completed" {
						r.UpdatedAt = time.Now()
					}
				}
			}
			out = append(out, *r)
		}
		return ghapi.Result{
			Runs: out,
			// Never reach zero: a spent budget would park the board until
			// the reset, which is not what the demo is for.
			RateLimit: ghapi.RateLimit{
				Remaining: max(4990-poll, 1),
				Reset:     time.Now().Add(time.Hour),
			},
		}
	}
}
