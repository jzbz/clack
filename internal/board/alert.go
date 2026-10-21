package board

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/jzbz/clack/internal/ghapi"
)

// hookTimeout bounds an --on-fail command, so a hook that hangs cannot pile
// up a goroutine and a child process on every poll.
const hookTimeout = 30 * time.Second

// isFailure reports whether a run went wrong in a way worth interrupting
// someone for. A run somebody cancelled on purpose, or one that was skipped,
// is not news; the board shows those but stays quiet about them.
func isFailure(r ghapi.Run) bool {
	if r.Status != "completed" {
		return false
	}
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	default:
		return false
	}
}

// notifier delivers one poll's worth of bad news.
type notifier func(failed []ghapi.Run) error

// newNotifier rings the terminal bell once for the batch, however many runs
// broke, and runs the hook once per run, because each is its own news.
func newNotifier(bell bool, hook string) notifier {
	if !bell && hook == "" {
		return nil
	}
	return func(failed []ghapi.Run) error {
		if bell {
			// A BEL is not a printable cell, so it cannot disturb the
			// frame the renderer has on screen.
			os.Stderr.WriteString("\a")
		}
		if hook == "" {
			return nil
		}
		var errs []error
		for _, r := range failed {
			if err := runHook(hook, r); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// runHook runs the user's --on-fail command with the run described in the
// environment. The run's own text is never spliced into the command: the
// command is the user's, a branch name is not.
func runHook(hook string, r ghapi.Run) error {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", hook)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", hook)
	}
	cmd.Env = append(os.Environ(),
		"CLACK_REPO="+r.Repo,
		"CLACK_WORKFLOW="+r.Workflow,
		"CLACK_BRANCH="+r.Branch,
		"CLACK_ACTOR="+r.Actor,
		"CLACK_STATUS="+r.Status,
		"CLACK_CONCLUSION="+r.Conclusion,
		"CLACK_URL="+r.URL,
		fmt.Sprintf("CLACK_RUN_ID=%d", r.ID),
	)
	// Output is swallowed, or the hook would scribble over the board.
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("on-fail %s: %w: %s", r.Repo, err, firstLine(out))
	}
	return nil
}

// firstLine is as much of a hook's output as the footer can show.
func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
