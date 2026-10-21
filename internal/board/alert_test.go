package board

import (
	"context"
	"errors"
	"fmt"
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

// rerunBoard returns a board with one finished and one live run, recording
// re-run requests instead of making them.
func rerunBoard(t *testing.T, err error) (*Model, *[]string) {
	t.Helper()
	var asked []string
	m := New(nil, Options{
		Interval: time.Minute, MaxRows: 10,
		Rerun: func(_ context.Context, repo string, id int64) error {
			asked = append(asked, fmt.Sprintf("%s#%d", repo, id))
			return err
		},
	})
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{
		{ID: 1, Repo: "acme/web", Workflow: "ci", Branch: "main",
			Status: "completed", Conclusion: "failure", StartedAt: time.Now()},
		{ID: 2, Repo: "acme/api", Workflow: "deploy", Branch: "main",
			Status: "in_progress", StartedAt: time.Now().Add(-time.Minute)},
	}}))
	return &m, &asked
}

// Nothing is sent until the question is answered yes.
func TestRerunNeedsConfirmation(t *testing.T) {
	m, asked := rerunBoard(t, nil)
	m.moveTo(1) // the finished run; the live one sorts first
	if m.selID != 1 {
		t.Fatalf("selID = %d, want the finished run 1", m.selID)
	}

	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.confirm == nil {
		t.Fatal("x did not ask")
	}
	if len(*asked) != 0 {
		t.Fatalf("asking already fired the re-run: %v", *asked)
	}
	if got := stripANSI(m.renderSelection()); !strings.Contains(got, "RE-RUN ACME/WEB") {
		t.Errorf("the question is not on screen: %q", got)
	}

	// Any other key is no.
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.confirm != nil || len(*asked) != 0 {
		t.Fatalf("n fired the re-run: confirm=%v asked=%v", m.confirm, *asked)
	}

	// Enter is not yes either: this one has to be meant.
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	*m = step(*m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(*asked) != 0 {
		t.Fatalf("enter fired the re-run: %v", *asked)
	}
}

func TestRerunOnYes(t *testing.T) {
	m, asked := rerunBoard(t, nil)
	m.moveTo(1)
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	*m = um.(Model)
	if cmd == nil {
		t.Fatal("y did not send anything")
	}
	msg := cmd()
	if _, ok := msg.(rerunMsg); !ok {
		t.Fatalf("got %T, want a rerunMsg", msg)
	}
	if len(*asked) != 1 || (*asked)[0] != "acme/web#1" {
		t.Fatalf("re-ran %v, want acme/web#1", *asked)
	}
	if m.confirm != nil {
		t.Error("the question is still up after answering")
	}
}

// A run still going cannot be re-run, and saying so beats a 403 later.
func TestRerunRefusesLiveRun(t *testing.T) {
	m, asked := rerunBoard(t, nil)
	m.moveTo(0) // the in-progress run
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.confirm != nil {
		t.Fatal("asked about a run that is still going")
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "finished") {
		t.Errorf("err = %v, want it to explain why not", m.err)
	}
	if len(*asked) != 0 {
		t.Fatalf("re-ran a live run: %v", *asked)
	}
}

// With no token there is nothing to re-run with; say so rather than ask.
func TestRerunUnavailable(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{
		{ID: 1, Repo: "acme/web", Status: "completed", Conclusion: "failure", StartedAt: time.Now()},
	}}))
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.confirm != nil {
		t.Fatal("asked without anything to ask with")
	}
	if m.err == nil {
		t.Error("no explanation given")
	}
}

// A refusal from GitHub belongs in the footer, and a success schedules the
// poll that will show the run boarding again.
func TestRerunResultHandling(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m = step(m, rerunMsg{errors.New("re-run acme/web: 403 Forbidden")})
	if m.err == nil || !strings.Contains(m.err.Error(), "403") {
		t.Errorf("refusal not surfaced: %v", m.err)
	}
	um, cmd := m.Update(rerunMsg{})
	if um.(Model).err != nil {
		t.Errorf("a successful re-run left an error: %v", um.(Model).err)
	}
	if cmd == nil {
		t.Error("a successful re-run did not schedule a poll")
	}
}

// The re-run question is modal. Enter is a plausible way to try to confirm
// it, and it is also the key that opens the selected run in a browser: when
// enter quietly meant "no", pressing it twice — which is what you do when a
// key seems not to have registered — opened a browser tab instead.
func TestRerunPromptIgnoresUnrelatedKeys(t *testing.T) {
	m, asked := rerunBoard(t, nil)
	for _, r := range m.rows {
		r.run.URL = "https://github.com/acme/web/actions/runs/1"
	}
	m.moveTo(1)
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	// Keys that are neither yes nor no leave the question exactly as it was.
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("o")},
		{Type: tea.KeyRunes, Runes: []rune("j")},
		{Type: tea.KeySpace, Runes: []rune{' '}},
	} {
		um, cmd := m.Update(key)
		*m = um.(Model)
		if cmd != nil {
			t.Fatalf("%q did something while the question was up", key.String())
		}
		if !m.askingRerun() {
			t.Fatalf("%q dismissed the question", key.String())
		}
	}
	if len(*asked) != 0 {
		t.Fatalf("re-ran without an answer: %v", *asked)
	}

	// n says no, and says so, so nobody presses it twice.
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.askingRerun() {
		t.Fatal("n left the question up")
	}
	if !strings.Contains(m.note, "CANCELLED") {
		t.Errorf("note = %q, want it to say the re-run was cancelled", m.note)
	}
	if got := stripANSI(m.renderSelection()); !strings.Contains(got, "CANCELLED") {
		t.Errorf("the cancellation is not on screen: %q", got)
	}

	// And the next keypress clears it again.
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.note != "" {
		t.Errorf("note survived the next keypress: %q", m.note)
	}
}

// esc answers the question rather than clearing the filter behind it.
func TestRerunPromptEscapeIsNo(t *testing.T) {
	m, asked := rerunBoard(t, nil)
	m.filter.mode = showFailed
	m.refilter()
	m.moveTo(0)
	*m = step(*m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	*m = step(*m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.askingRerun() {
		t.Fatal("esc left the question up")
	}
	if !m.filter.active() {
		t.Error("esc cleared the filter as well as answering the question")
	}
	if len(*asked) != 0 {
		t.Fatalf("esc re-ran something: %v", *asked)
	}
}
