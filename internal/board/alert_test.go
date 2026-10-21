package board

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzbz/clack/internal/ghapi"
)

func TestIsFailure(t *testing.T) {
	cases := []struct {
		status, concl string
		want          bool
	}{
		{"completed", "failure", true},
		{"completed", "timed_out", true},
		{"completed", "startup_failure", true},
		{"completed", "success", false},
		{"completed", "skipped", false},
		{"completed", "action_required", false},
		// Somebody cancelled this on purpose. Not news.
		{"completed", "cancelled", false},
		// Not over yet, whatever the stale conclusion says.
		{"in_progress", "failure", false},
		{"queued", "", false},
	}
	for _, c := range cases {
		got := isFailure(ghapi.Run{Status: c.status, Conclusion: c.concl})
		if got != c.want {
			t.Errorf("isFailure(%q, %q) = %v, want %v", c.status, c.concl, got, c.want)
		}
	}
}

// alerting returns a board that records what it would have alerted on
// instead of ringing the terminal or running a command.
func alerting(t *testing.T, maxRows int) (*Model, *[][]ghapi.Run) {
	t.Helper()
	var got [][]ghapi.Run
	m := New(nil, Options{Interval: time.Minute, MaxRows: maxRows})
	m.notify = func(failed []ghapi.Run) error {
		got = append(got, failed)
		return nil
	}
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	return &m, &got
}

// poll feeds the board one poll result and delivers any alert it raises.
func poll(t *testing.T, m *Model, runs ...ghapi.Run) {
	t.Helper()
	failed := m.merge(runs)
	if cmd := m.alertCmd(failed); cmd != nil {
		if msg, ok := cmd().(alertMsg); ok && msg.err != nil {
			t.Errorf("notify: %v", msg.err)
		}
	}
}

// drain runs cmd and collects up to want messages, giving up after d. A
// batch from the board holds poll and frame timers that will not fire for
// seconds; this picks up only the commands that answer straight away.
func drain(cmd tea.Cmd, want int, d time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msgs := make(chan tea.Msg, 8)
	switch batch := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range batch {
			go func() {
				if msg := c(); msg != nil {
					msgs <- msg
				}
			}()
		}
	case nil:
		return nil
	default:
		return []tea.Msg{batch}
	}
	var out []tea.Msg
	deadline := time.After(d)
	for {
		select {
		case msg := <-msgs:
			if out = append(out, msg); len(out) == want {
				return out
			}
		case <-deadline:
			return out
		}
	}
}

// A failure arriving through the real update loop must reach the notifier,
// not just through merge.
func TestAlertReachesNotifierThroughUpdate(t *testing.T) {
	var got [][]ghapi.Run
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m.notify = func(failed []ghapi.Run) error {
		got = append(got, failed)
		return nil
	}
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{run(1, "in_progress", "")}}))

	um, cmd := m.Update(runsMsg(ghapi.Result{Runs: []ghapi.Run{run(1, "completed", "failure")}}))
	m = um.(Model)
	drain(cmd, 1, 2*time.Second)

	if len(got) != 1 || len(got[0]) != 1 || got[0][0].ID != 1 {
		t.Fatalf("the update loop did not alert: %v", got)
	}
}

func run(id int64, status, concl string) ghapi.Run {
	return ghapi.Run{
		ID: id, Repo: "acme/web", Workflow: "ci", Branch: "main",
		Status: status, Conclusion: concl, StartedAt: time.Now(),
	}
}

// The first poll seeds the board from history. Everything red on it already
// happened, so ringing the bell for all of it at startup would be wrong.
func TestNoAlertOnFirstPoll(t *testing.T) {
	m, got := alerting(t, 10)
	poll(t, m, run(1, "completed", "failure"), run(2, "completed", "failure"))
	if len(*got) != 0 {
		t.Fatalf("the seeding poll alerted: %v", *got)
	}

	// A new failure after that is news.
	poll(t, m, run(1, "completed", "failure"), run(3, "completed", "failure"))
	if len(*got) != 1 || len((*got)[0]) != 1 || (*got)[0][0].ID != 3 {
		t.Fatalf("expected one alert for run 3, got %v", *got)
	}
}

func TestAlertOnTransitionToFailure(t *testing.T) {
	m, got := alerting(t, 10)
	poll(t, m, run(1, "in_progress", ""))
	poll(t, m, run(1, "completed", "failure"))
	if len(*got) != 1 || (*got)[0][0].ID != 1 {
		t.Fatalf("a run going red did not alert: %v", *got)
	}

	// Still red on the next poll: already reported, stay quiet.
	poll(t, m, run(1, "completed", "failure"))
	if len(*got) != 1 {
		t.Fatalf("the same failure alerted twice: %v", *got)
	}
}

func TestNoAlertForCancelledOrPassing(t *testing.T) {
	m, got := alerting(t, 10)
	poll(t, m, run(1, "in_progress", ""), run(2, "in_progress", ""))
	poll(t, m, run(1, "completed", "cancelled"), run(2, "completed", "success"))
	if len(*got) != 0 {
		t.Fatalf("a cancelled or passing run alerted: %v", *got)
	}
}

// A failure pushed off the bottom of the board by busier repos still gets
// its one alert, and does not get a second when it bobs back into view.
func TestAlertSurvivesRowTrimming(t *testing.T) {
	m, got := alerting(t, 1)
	poll(t, m, run(1, "in_progress", ""))

	older := run(2, "completed", "failure")
	older.StartedAt = time.Now().Add(-time.Hour) // sorts below, trimmed off
	poll(t, m, run(1, "in_progress", ""), older)
	if len(*got) != 1 || (*got)[0][0].ID != 2 {
		t.Fatalf("a trimmed-off failure was not reported: %v", *got)
	}
	if w := m.window(); len(w) != 1 || w[0].id != 1 {
		t.Fatalf("maxRows not honoured on screen: %+v", w)
	}

	poll(t, m, older, run(1, "in_progress", ""))
	if len(*got) != 1 {
		t.Fatalf("the same failure alerted again once visible: %v", *got)
	}
}

// Once GitHub stops reporting a run, forget it, or the set grows forever.
func TestAlertedSetIsPruned(t *testing.T) {
	m, _ := alerting(t, 10)
	poll(t, m, run(1, "completed", "failure"))
	if len(m.alerted) != 1 {
		t.Fatalf("alerted = %d, want 1", len(m.alerted))
	}
	poll(t, m, run(9, "in_progress", ""))
	if len(m.alerted) != 0 {
		t.Fatalf("alerted still holds %d runs after they left the window", len(m.alerted))
	}
}

// A failing hook reports itself in the footer rather than disappearing.
func TestAlertErrorSurfaces(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m = step(m, alertMsg{errors.New("on-fail acme/web: exit status 1")})
	if m.err == nil || !strings.Contains(m.err.Error(), "exit status 1") {
		t.Fatalf("hook failure not surfaced: %v", m.err)
	}
}

// With neither the bell nor a hook wanted, there is nothing to deliver.
func TestNoNotifierWhenAlertsOff(t *testing.T) {
	if newNotifier(false, "") != nil {
		t.Error("a notifier was built with nothing to do")
	}
	if newNotifier(false, "true") == nil {
		t.Error("a hook alone did not build a notifier")
	}
}

// The hook gets the run in its environment, never spliced into the command.
func TestRunHookEnvironment(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	out := t.TempDir() + "/hook.out"
	r := ghapi.Run{
		ID: 42, Repo: "acme/web", Workflow: "ci", Branch: "fix/css",
		Actor: "mona", Status: "completed", Conclusion: "failure",
		URL: "https://github.com/acme/web/actions/runs/42",
	}
	if err := runHook("printf '%s %s %s %s' \"$CLACK_REPO\" \"$CLACK_BRANCH\" \"$CLACK_CONCLUSION\" \"$CLACK_RUN_ID\" > "+out, r); err != nil {
		t.Fatalf("runHook: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "acme/web fix/css failure 42"; string(b) != want {
		t.Errorf("hook saw %q, want %q", b, want)
	}
}

// A branch name is data, not shell. It must not reach the command line.
func TestRunHookDoesNotInterpolateRunText(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	out := t.TempDir() + "/hook.out"
	r := ghapi.Run{ID: 1, Repo: "acme/web", Branch: "x; echo pwned"}
	if err := runHook("printf '%s' \"$CLACK_BRANCH\" > "+out, r); err != nil {
		t.Fatalf("runHook: %v", err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "x; echo pwned" {
		t.Errorf("hook saw %q; the branch was not passed through intact", b)
	}
}

func TestRunHookReportsFailure(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	err := runHook("echo something broke >&2; exit 3", ghapi.Run{Repo: "acme/web"})
	if err == nil {
		t.Fatal("a hook exiting non-zero reported success")
	}
	if !strings.Contains(err.Error(), "acme/web") || !strings.Contains(err.Error(), "something broke") {
		t.Errorf("error %q lacks the repo or the hook's own output", err)
	}
}

// Driven through the demo's whole script, the board should ring exactly
// once: run 1002 fails at poll 8. Run 1005 is already red when the board
// seeds, and run 1007 is cancelled on purpose at poll 12 — neither is news.
func TestAlertsAcrossTheDemoScript(t *testing.T) {
	var alerted []int64
	fetch := Demo()
	m := New(fetch, Options{Interval: time.Minute, MaxRows: 12})
	m.notify = func(failed []ghapi.Run) error {
		for _, r := range failed {
			alerted = append(alerted, r.ID)
		}
		return nil
	}
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})

	for range 14 {
		res := fetch(t.Context())
		failed := m.merge(res.Runs)
		if cmd := m.alertCmd(failed); cmd != nil {
			cmd()
		}
	}
	if len(alerted) != 1 || alerted[0] != 1002 {
		t.Fatalf("alerted on %v, want exactly [1002]", alerted)
	}
}

// The notifier the board actually builds must reach the hook, once per run.
func TestNotifierRunsHookPerRun(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	out := t.TempDir() + "/fired"
	n := newNotifier(false, "printf '%s\\n' \"$CLACK_REPO\" >> "+out)
	if n == nil {
		t.Fatal("no notifier built for a hook")
	}
	if err := n([]ghapi.Run{
		{ID: 1, Repo: "acme/web"},
		{ID: 2, Repo: "acme/api"},
	}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "acme/web\nacme/api\n"; got != want {
		t.Errorf("hook fired %q, want %q", got, want)
	}
}
