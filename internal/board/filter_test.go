package board

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzbz/clack/internal/ghapi"
)

// board returns a board holding one run of each interesting kind.
func filterBoard(t *testing.T) Model {
	t.Helper()
	now := time.Now()
	m := New(nil, Options{Interval: time.Minute, MaxRows: 20})
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 40})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{
		{ID: 1, Repo: "acme/web", Workflow: "ci", Branch: "main", Actor: "octocat",
			Status: "in_progress", StartedAt: now},
		{ID: 2, Repo: "acme/api", Workflow: "deploy", Branch: "release/v2", Actor: "hubot",
			Status: "queued", StartedAt: now.Add(-time.Minute)},
		{ID: 3, Repo: "acme/web", Workflow: "lint", Branch: "fix/css", Actor: "mona",
			Status: "completed", Conclusion: "failure", StartedAt: now.Add(-2 * time.Minute)},
		{ID: 4, Repo: "acme/infra", Workflow: "terraform", Branch: "main", Actor: "octocat",
			Status: "completed", Conclusion: "success", StartedAt: now.Add(-3 * time.Minute)},
		{ID: 5, Repo: "acme/mobile", Workflow: "tests", Branch: "feat/tabs", Actor: "mona",
			Status: "completed", Conclusion: "action_required", StartedAt: now.Add(-4 * time.Minute)},
	}}))
	return m
}

func shownIDs(m Model) []int64 {
	ids := make([]int64, 0, len(m.shown))
	for _, r := range m.shown {
		ids = append(ids, r.id)
	}
	return ids
}

func equalIDs(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterModes(t *testing.T) {
	m := filterBoard(t)
	if got := shownIDs(m); !equalIDs(got, 1, 2, 3, 4, 5) {
		t.Fatalf("unfiltered board shows %v, want all five", got)
	}

	// f cycles ALL -> LIVE -> FAILED -> ALL.
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.filter.mode != showLive {
		t.Fatalf("mode = %v, want LIVE", m.filter.mode)
	}
	if got := shownIDs(m); !equalIDs(got, 1, 2) {
		t.Errorf("LIVE shows %v, want the in-progress and queued runs", got)
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.filter.mode != showFailed {
		t.Fatalf("mode = %v, want FAILED", m.filter.mode)
	}
	// Red and needs-attention, but not the success and not the live runs.
	if got := shownIDs(m); !equalIDs(got, 3, 5) {
		t.Errorf("FAILED shows %v, want the failure and the held run", got)
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.filter.mode != showAll {
		t.Fatalf("mode = %v, want back to ALL", m.filter.mode)
	}
}

// A deliberate cancel stays off the bell but belongs in the FAILED view:
// when you ask what went wrong, you want to see it.
func TestFailedViewIncludesCancelled(t *testing.T) {
	cancelled := ghapi.Run{ID: 9, Repo: "acme/web", Status: "completed", Conclusion: "cancelled"}
	f := filter{mode: showFailed}
	if !f.match(cancelled, statusOf(cancelled)) {
		t.Error("FAILED hid a cancelled run")
	}
	if isFailure(cancelled) {
		t.Error("a cancelled run should not ring the bell")
	}
}

func TestFilterQuery(t *testing.T) {
	cases := []struct {
		query string
		want  []int64
	}{
		{"web", []int64{1, 3}},           // repo
		{"deploy", []int64{2}},           // workflow
		{"fix/css", []int64{3}},          // branch
		{"mona", []int64{3, 5}},          // actor
		{"ACME", []int64{1, 2, 3, 4, 5}}, // case-insensitive
		{"nothing", nil},
	}
	for _, c := range cases {
		m := filterBoard(t)
		m.filter.setQuery(c.query)
		m.refilter()
		if got := shownIDs(m); !equalIDs(got, c.want...) {
			t.Errorf("query %q shows %v, want %v", c.query, got, c.want)
		}
	}
}

// Typing a query goes through the keyboard, not straight into the struct.
func TestQueryTyping(t *testing.T) {
	m := filterBoard(t)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.typing {
		t.Fatal("/ did not open the search")
	}
	for _, r := range "web" {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.filter.query != "web" {
		t.Fatalf("query = %q, want web", m.filter.query)
	}
	if got := shownIDs(m); !equalIDs(got, 1, 3) {
		t.Errorf("typing filtered to %v, want 1 and 3", got)
	}

	// Backspace narrows back out.
	m = step(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter.query != "we" {
		t.Fatalf("after backspace query = %q, want we", m.filter.query)
	}

	// Enter keeps it and hands the keyboard back to the board.
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.typing || m.filter.query != "we" {
		t.Fatalf("enter: typing=%v query=%q", m.typing, m.filter.query)
	}

	// A letter now moves the board again rather than typing.
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.filter.query != "we" {
		t.Fatalf("j was typed into the query: %q", m.filter.query)
	}
}

// Space is its own key type in bubbletea but still carries its rune.
func TestQueryAcceptsSpace(t *testing.T) {
	m := filterBoard(t)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = step(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.filter.query != "a b" {
		t.Fatalf("query = %q, want %q", m.filter.query, "a b")
	}
}

// Escape out of the search puts back whatever was there before.
func TestQueryEscapeRestores(t *testing.T) {
	m := filterBoard(t)
	m.filter.setQuery("web")
	m.refilter()

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "xyz" {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.typing || m.filter.query != "web" {
		t.Fatalf("escape left typing=%v query=%q, want the previous web", m.typing, m.filter.query)
	}
	if got := shownIDs(m); !equalIDs(got, 1, 3) {
		t.Errorf("escape left the board showing %v", got)
	}
}

// Escape clears a filter before it quits, so one key backs out of a search
// without killing the board.
func TestEscapeClearsFilterThenQuits(t *testing.T) {
	m := filterBoard(t)
	m.filter.mode = showFailed
	m.refilter()

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = um.(Model)
	if cmd != nil {
		t.Fatal("escape quit a filtered board instead of clearing the filter")
	}
	if m.filter.active() {
		t.Fatalf("filter still active: %+v", m.filter)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); cmd == nil {
		t.Fatal("escape on an unfiltered board did not quit")
	}
}

// The selection has to land somewhere sensible when the rows under it go.
func TestSelectionFollowsFilter(t *testing.T) {
	m := filterBoard(t)
	m.moveTo(3) // run 4, the successful one
	if m.selID != 4 {
		t.Fatalf("selID = %d, want 4", m.selID)
	}
	m.filter.mode = showFailed
	m.refilter()
	if m.selID != 3 {
		t.Fatalf("selID = %d, want the first visible row 3", m.selID)
	}
	// Back to everything: the selection stays where it was left.
	m.filter.clear()
	m.refilter()
	if m.selID != 3 {
		t.Fatalf("selID = %d, want 3 kept", m.selID)
	}
}

// Opening a run must open the selected one, not the third row of some
// earlier, unfiltered board.
func TestSelectedRespectsFilter(t *testing.T) {
	m := filterBoard(t)
	m.filter.mode = showFailed
	m.refilter()
	got := m.selected()
	if got == nil || got.id != 3 {
		t.Fatalf("selected = %+v, want run 3", got)
	}
}

func TestScrollFollowsSelection(t *testing.T) {
	now := time.Now()
	runs := make([]ghapi.Run, 30)
	for i := range runs {
		runs[i] = ghapi.Run{
			ID: int64(i + 1), Repo: "acme/web", Workflow: "ci", Status: "queued",
			StartedAt: now.Add(-time.Duration(i) * time.Minute),
		}
	}
	m := New(nil, Options{Interval: time.Minute, MaxRows: 5})
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 40})
	m = step(m, runsMsg(ghapi.Result{Runs: runs}))

	if got := len(m.window()); got != 5 {
		t.Fatalf("window = %d, want 5", got)
	}
	if m.top != 0 {
		t.Fatalf("top = %d, want 0", m.top)
	}

	// Moving inside the window does not scroll.
	m.moveTo(4)
	if m.top != 0 {
		t.Fatalf("top = %d after moving to the last visible row, want 0", m.top)
	}
	// Moving past it scrolls by the least it can.
	m.move(1)
	if m.top != 1 {
		t.Fatalf("top = %d after stepping below the window, want 1", m.top)
	}
	// The end shows the last page, and the selection is on screen.
	m.moveTo(len(m.shown) - 1)
	if m.top != 25 {
		t.Fatalf("top = %d at the bottom, want 25", m.top)
	}
	if !windowHas(m, m.selID) {
		t.Fatal("the selected row is off screen")
	}
	// And back to the top.
	m.moveTo(0)
	if m.top != 0 || !windowHas(m, m.selID) {
		t.Fatalf("top = %d back at the start", m.top)
	}
}

func windowHas(m Model, id int64) bool {
	for _, r := range m.window() {
		if r.id == id {
			return true
		}
	}
	return false
}

// A filtered or scrolled board still has to say so, or it just looks quiet.
func TestFilterLineAndPosition(t *testing.T) {
	m := filterBoard(t)
	m.filter.mode = showFailed
	m.refilter()
	if got := m.filterLine(); !strings.Contains(got, "FAILED") || !strings.Contains(got, "2 OF 5") {
		t.Errorf("filter line = %q", got)
	}
	m.typing = true
	m.filter.setQuery("css")
	if got := m.filterLine(); !strings.Contains(got, "/CSS") {
		t.Errorf("typing line = %q", got)
	}

	// Frames stay exactly the terminal width with the extra line in play.
	m.typing = false
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		if got := len([]rune(line)); got != 110 {
			t.Errorf("line %q is %d cells, want 110", line, got)
		}
	}
}

// The three ways a run can end badly are told apart, and the FAILED view
// collects all of them while the bell is choosier.
func TestStatusSplitFeedsFilterAndAlert(t *testing.T) {
	cases := []struct {
		concl    string
		word     string
		inFailed bool
		rings    bool
	}{
		{"failure", "FAILED", true, true},
		{"startup_failure", "FAILED", true, true},
		{"timed_out", "DELAYED", true, true},
		{"cancelled", "CANCELLED", true, false},
		{"action_required", "HOLD", true, false},
		{"success", "DEPARTED", false, false},
		{"skipped", "SKIPPED", false, false},
	}
	f := filter{mode: showFailed}
	for _, c := range cases {
		r := ghapi.Run{ID: 1, Repo: "acme/web", Status: "completed", Conclusion: c.concl}
		st := statusOf(r)
		if st.String() != c.word {
			t.Errorf("%s shows %q, want %q", c.concl, st, c.word)
		}
		if got := f.match(r, st); got != c.inFailed {
			t.Errorf("%s in FAILED view = %v, want %v", c.concl, got, c.inFailed)
		}
		if got := isFailure(r); got != c.rings {
			t.Errorf("%s rings = %v, want %v", c.concl, got, c.rings)
		}
	}
}
