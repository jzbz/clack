package board

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzbz/clack/internal/ghapi"
)

// step delivers msg to m and returns the updated model.
func step(m Model, msg tea.Msg) Model {
	um, _ := m.Update(msg)
	return um.(Model)
}

// frame advances the board one repaint, whatever generation its clock is on.
func frame(m Model) Model { return step(m, frameMsg{m.frame}) }

// frames advances the board n repaints.
func frames(m Model, n int) Model {
	for range n {
		m = frame(m)
	}
	return m
}

func TestSortRuns(t *testing.T) {
	now := time.Now()
	runs := []ghapi.Run{
		{ID: 1, Status: "completed", Conclusion: "success", StartedAt: now.Add(-time.Hour)},
		{ID: 2, Status: "queued", StartedAt: now.Add(-time.Minute)},
		{ID: 3, Status: "in_progress", StartedAt: now.Add(-10 * time.Minute)},
		{ID: 4, Status: "in_progress", StartedAt: now.Add(-2 * time.Minute)},
		{ID: 5, Status: "completed", Conclusion: "failure", StartedAt: now.Add(-5 * time.Minute)},
	}
	sortRuns(runs)
	want := []int64{4, 3, 2, 5, 1}
	for i, id := range want {
		if runs[i].ID != id {
			t.Fatalf("position %d: got run %d, want %d (order %v)", i, runs[i].ID, id, want)
		}
	}
}

func TestStatusOf(t *testing.T) {
	cases := []struct {
		status, concl, want string
	}{
		{"in_progress", "", "BOARDING"},
		{"queued", "", "QUEUED"},
		{"requested", "", "QUEUED"},
		{"waiting", "", "WAITING"},
		{"completed", "success", "DEPARTED"},
		{"completed", "failure", "CANCELLED"},
		{"completed", "cancelled", "CANCELLED"},
		{"completed", "timed_out", "CANCELLED"},
		{"completed", "skipped", "SKIPPED"},
		{"completed", "action_required", "HOLD"},
		{"completed", "", "ENDED"},
	}
	for _, c := range cases {
		got := statusOf(ghapi.Run{Status: c.status, Conclusion: c.concl}).String()
		if got != c.want {
			t.Errorf("statusOf(%q, %q) = %q, want %q", c.status, c.concl, got, c.want)
		}
	}
}

// Every status needs a word and a style, or the board indexes past the table.
func TestStatusTablesComplete(t *testing.T) {
	for s := status(0); s < numStatuses; s++ {
		if statusWords[s] == "" {
			t.Errorf("status %d has no word", s)
		}
		if statusStyle[s] == idBlank {
			t.Errorf("status %q has no style", s)
		}
	}
}

func TestShortDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0S"},
		{0, "0S"},
		{45 * time.Second, "45S"},
		{59 * time.Second, "59S"},
		{time.Minute, "1M00S"},
		{12*time.Minute + 5*time.Second, "12M05S"},
		{time.Hour, "1H00M"},
		{3*time.Hour + 4*time.Minute, "3H04M"},
	}
	for _, c := range cases {
		if got := shortDur(c.d); got != c.want {
			t.Errorf("shortDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ci", "CI   "},
		{"", "     "},
		{"exactly", "EXACT"},
	}
	for _, c := range cases {
		if got := fit(c.in, 5); got != c.want {
			t.Errorf("fit(%q, 5) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := fit("feature/long-branch-name", 8); got != "FEATURE/" {
		t.Errorf("fit truncate: got %q", got)
	}
	if got := fit("ab", 2); got != "AB" {
		t.Errorf("fit exact: got %q", got)
	}
	if got := fit("x", 0); got != "" {
		t.Errorf("fit zero width: got %q", got)
	}
}

// A character the board has no flap for still occupies exactly one cell, so
// an emoji in a workflow name cannot shove later columns out of line.
func TestFitKeepsOneCellPerGlyph(t *testing.T) {
	for _, in := range []string{"🚀 deploy", "日本語", "build ✓ test", "naïve"} {
		got := fit(in, 10)
		if n := len([]rune(got)); n != 10 {
			t.Errorf("fit(%q, 10) = %q: %d runes, want 10", in, got, n)
		}
		for _, r := range got {
			if rampPos(r) < 0 {
				t.Errorf("fit(%q, 10) = %q: glyph %q is not on the ramp", in, got, r)
			}
		}
	}
}

func TestColumnsFitWidth(t *testing.T) {
	for _, w := range []int{40, 50, 70, 90, 110, 160} {
		cols := columns(w)
		if total := totalWidth(cols); total > w {
			t.Errorf("width %d: columns total %d overflows", w, total)
		}
		// The fields that carry the news are never dropped.
		for _, want := range []colKind{colRepo, colWorkflow, colStatus, colAge} {
			if !hasColumn(cols, want) {
				t.Errorf("width %d: column %d missing", w, want)
			}
		}
	}
	// A wide board spends its slack on the workflow column.
	for _, c := range columns(160) {
		if c.kind == colWorkflow && c.width <= minWorkflow {
			t.Errorf("wide board did not flex the workflow column: %d", c.width)
		}
	}
}

func hasColumn(cols []column, k colKind) bool {
	for _, c := range cols {
		if c.kind == k {
			return true
		}
	}
	return false
}

func TestMergeKeepsRowIdentity(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m.width = 100
	run := ghapi.Run{ID: 7, Repo: "acme/web", Status: "queued", StartedAt: time.Now()}
	m.merge([]ghapi.Run{run})
	first := m.rows[0]
	m.merge([]ghapi.Run{run})
	if m.rows[0] != first {
		t.Fatal("merge dropped row identity; flaps would restart each poll")
	}
	// A run that disappears from the results leaves the board.
	m.merge([]ghapi.Run{{ID: 8, Status: "queued", StartedAt: time.Now()}})
	if len(m.rows) != 1 || m.rows[0].id != 8 {
		t.Fatalf("stale run not evicted: %+v", m.rows)
	}
	if len(m.byID) != 1 {
		t.Fatalf("index still holds %d rows, want 1", len(m.byID))
	}
}

// maxRows caps what is drawn, not what is kept: the rest has to stay around
// for filtering and scrolling to reach.
func TestMaxRowsCapsThePanelNotTheBoard(t *testing.T) {
	now := time.Now()
	m := New(nil, Options{Interval: time.Minute, MaxRows: 2})
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.merge([]ghapi.Run{
		{ID: 1, Status: "queued", StartedAt: now},
		{ID: 2, Status: "queued", StartedAt: now.Add(-time.Minute)},
		{ID: 3, Status: "queued", StartedAt: now.Add(-2 * time.Minute)},
	})
	if len(m.rows) != 3 || len(m.byID) != 3 {
		t.Fatalf("rows=%d index=%d, want 3 and 3 kept", len(m.rows), len(m.byID))
	}
	if len(m.shown) != 3 {
		t.Fatalf("shown=%d, want all 3 past the filter", len(m.shown))
	}
	if got := len(m.window()); got != 2 {
		t.Fatalf("window = %d rows, want maxRows 2", got)
	}
}

func TestSelection(t *testing.T) {
	now := time.Now()
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m.width = 100
	runs := []ghapi.Run{
		{ID: 1, Status: "completed", Conclusion: "success", StartedAt: now},
		{ID: 2, Status: "completed", Conclusion: "success", StartedAt: now.Add(-time.Hour)},
	}
	m.merge(runs)
	if m.selID != 1 {
		t.Fatalf("default selection = %d, want top row 1", m.selID)
	}

	m.move(1)
	if m.selID != 2 {
		t.Fatalf("after move(1) selID = %d, want 2", m.selID)
	}
	m.move(5)
	if m.selID != 2 {
		t.Fatalf("move past bottom: selID = %d, want clamped 2", m.selID)
	}
	m.move(-9)
	if m.selID != 1 {
		t.Fatalf("move past top: selID = %d, want clamped 1", m.selID)
	}
	m.moveTo(len(m.rows) - 1)
	if m.selID != 2 {
		t.Fatalf("moveTo bottom: selID = %d, want 2", m.selID)
	}
	m.moveTo(0)
	if m.selID != 1 {
		t.Fatalf("moveTo top: selID = %d, want 1", m.selID)
	}

	// The selection follows its run when the board reorders.
	m.move(1) // select run 2
	m.merge([]ghapi.Run{
		{ID: 2, Status: "completed", Conclusion: "success", StartedAt: now},
		{ID: 1, Status: "completed", Conclusion: "success", StartedAt: now.Add(-time.Hour)},
	})
	if got := m.selected(); got == nil || got.id != 2 {
		t.Fatalf("selection lost on reorder: got %+v, want run 2", got)
	}

	// When the selected run leaves the board, the top row takes over.
	m.merge([]ghapi.Run{{ID: 9, Status: "queued", StartedAt: now}})
	if m.selID != 9 {
		t.Fatalf("selection after eviction = %d, want 9", m.selID)
	}

	// An empty board clears the selection.
	m.merge(nil)
	if m.selID != 0 || m.selected() != nil {
		t.Fatalf("selection after empty merge: selID=%d selected=%+v", m.selID, m.selected())
	}
}

// A clean poll that finds nothing empties the board; a failed one keeps the
// last good picture rather than blanking it on a blip.
func TestEmptyResultHandling(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{{ID: 1, Status: "queued", StartedAt: time.Now()}}}))
	if len(m.rows) != 1 {
		t.Fatalf("rows after first poll = %d, want 1", len(m.rows))
	}

	m = step(m, runsMsg(ghapi.Result{Err: errors.New("boom")}))
	if len(m.rows) != 1 {
		t.Fatalf("failed poll emptied the board: rows = %d, want 1", len(m.rows))
	}
	if m.failures != 1 {
		t.Fatalf("failures = %d, want 1", m.failures)
	}

	m = step(m, runsMsg(ghapi.Result{}))
	if len(m.rows) != 0 {
		t.Fatalf("clean empty poll left %d rows, want 0", len(m.rows))
	}
	if m.err != nil || m.failures != 0 {
		t.Fatalf("clean poll did not clear the error: err=%v failures=%d", m.err, m.failures)
	}
}

func TestNextPollInBacksOff(t *testing.T) {
	m := New(nil, Options{Interval: 30 * time.Second, MaxRows: 10})
	m.now = time.Now()
	if got := m.nextPollIn(); got != 30*time.Second {
		t.Errorf("healthy interval = %v, want 30s", got)
	}
	m.failures = 2
	if got := m.nextPollIn(); got != 2*time.Minute {
		t.Errorf("two failures = %v, want 2m", got)
	}
	m.failures = 100
	if got := m.nextPollIn(); got != maxBackoff {
		t.Errorf("many failures = %v, want %v", got, maxBackoff)
	}
	// A spent budget waits for the reset instead of backing off.
	m.failures = 0
	m.rl = ghapi.RateLimit{Remaining: 0, Reset: m.now.Add(10 * time.Minute)}
	if got := m.nextPollIn(); got < 10*time.Minute {
		t.Errorf("rate limited = %v, want at least 10m", got)
	}
}

// The frame clock is the board's main cost: a settled board must drop out of
// animation speed instead of repainting twenty times a second forever.
func TestNextFrameSlowsWhenSettled(t *testing.T) {
	// Pin the clock to a whole second, which is also a whole blink period,
	// so the next frame is an exact period away rather than whatever the
	// real clock happens to be a few milliseconds short of.
	base := time.Unix(1800000000, 0)
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m.clock = func() time.Time { return base }
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{
		{ID: 1, Repo: "acme/web", Status: "completed", Conclusion: "success", StartedAt: base},
	}}))
	if got := m.nextFrame(); got != animFrame {
		t.Fatalf("a moving board schedules %v, want %v", got, animFrame)
	}
	m = frames(m, 500)
	for _, r := range m.rows {
		if !r.fl.settled() {
			t.Fatal("board never settled")
		}
	}
	if got := m.nextFrame(); got != idleFrame {
		t.Fatalf("a settled board schedules %v, want %v", got, idleFrame)
	}

	// A blinking BOARDING row wakes often enough to blink, but still well
	// short of animation speed.
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{
		{ID: 2, Repo: "acme/web", Status: "in_progress", StartedAt: base},
	}}))
	m = frames(m, 500)
	if got := m.nextFrame(); got != blinkPeriod {
		t.Fatalf("blinking board schedules %v, want %v", got, blinkPeriod)
	}
}

// Only one frame clock may run: a tick from a superseded schedule is dropped,
// and every schedule that supersedes one starts a replacement.
func TestFrameClockIsSingular(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	stale := m.frame - 1

	um, cmd := m.Update(frameMsg{stale})
	if cmd != nil {
		t.Fatal("a stale frame tick started a second clock")
	}
	if um.(Model).frame != m.frame {
		t.Fatal("a stale frame tick advanced the generation")
	}

	um, cmd = m.Update(frameMsg{m.frame})
	if cmd == nil {
		t.Fatal("the live frame tick did not schedule the next one")
	}
	if um.(Model).frame == m.frame {
		t.Fatal("the live frame tick did not supersede itself")
	}
}

// errors.Join separates its parts with newlines, which would tear the frame.
func TestOneLineFlattensJoinedErrors(t *testing.T) {
	err := errors.Join(errors.New("acme/web: 404"), errors.New("acme/api:\n401"))
	got := oneLine(err)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("oneLine kept whitespace: %q", got)
	}
	if want := "! acme/web: 404 acme/api: 401"; got != want {
		t.Fatalf("oneLine = %q, want %q", got, want)
	}
}

func TestClampRunes(t *testing.T) {
	if got := clampRunes("日本語テスト", 3); got != "日本語" {
		t.Errorf("clampRunes multibyte = %q", got)
	}
	if got := clampRunes("abc", 10); got != "abc" {
		t.Errorf("clampRunes short = %q", got)
	}
	if got := clampRunes("abc", 0); got != "" {
		t.Errorf("clampRunes zero = %q", got)
	}
}

// Every line of a frame must be exactly the terminal's width, or the board
// wraps and smears as it redraws.
func TestViewLinesAreExactlyWidth(t *testing.T) {
	fetch := Demo()
	for _, w := range []int{60, 80, 110, 200} {
		m := New(fetch, Options{Interval: time.Minute, MaxRows: 6})
		m = step(m, tea.WindowSizeMsg{Width: w, Height: 24})
		m = step(m, runsMsg(fetch(t.Context())))
		m.showHelp = true
		m.err = errors.New("acme/web: 404\nacme/api: 401")
		for _, n := range []int{3, 200} {
			m = frames(m, n)
			for i, line := range strings.Split(stripANSI(m.View()), "\n") {
				if got := len([]rune(line)); got != w {
					t.Errorf("width %d, frame %d, line %d: %d cells, want %d (%q)",
						w, n, i, got, w, line)
				}
			}
		}
	}
}

// The panel keeps a fixed height, and the whole frame fits the terminal.
func TestViewFitsHeight(t *testing.T) {
	fetch := Demo()
	for _, h := range []int{10, 14, 24, 40} {
		for _, help := range []bool{false, true} {
			m := New(fetch, Options{Interval: time.Minute, MaxRows: 12})
			m = step(m, tea.WindowSizeMsg{Width: 110, Height: h})
			m = step(m, runsMsg(fetch(t.Context())))
			m.showHelp = help
			lines := strings.Count(m.View(), "\n") + 1
			if lines > h {
				t.Errorf("height %d (help=%v): frame is %d lines", h, help, lines)
			}
		}
	}
}

// An empty board still fills its panel, so the footer does not jump about.
func TestViewEmptyBoardKeepsPanelHeight(t *testing.T) {
	m := New(nil, Options{Interval: time.Minute, MaxRows: 5})
	m = step(m, tea.WindowSizeMsg{Width: 80, Height: 40})
	full := strings.Count(m.View(), "\n")
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{{ID: 1, Status: "queued", StartedAt: time.Now()}}}))
	if got := strings.Count(m.View(), "\n"); got != full {
		t.Fatalf("panel height changed with content: %d then %d", full, got)
	}
}

// The painted fast path must emit byte-for-byte what lipgloss.Style.Render
// would have, or the optimisation is a rewrite of the board's appearance.
func TestPaintMatchesLipgloss(t *testing.T) {
	segments := []string{
		"", " ", "CI   ", "ACME/WEB            ", "BOARDING   ",
		"  NO FLIGHTS ON THE BOARD", "↑/K ↓/J SELECT · Q QUIT", "─────",
		strings.Repeat(" ", 200),
	}
	for _, styles := range []styleSet{buildStyles(bgColor), buildStyles(selBgColor)} {
		for id := styleID(0); id < numStyleIDs; id++ {
			p := newPaint(styles[id])
			for _, seg := range segments {
				if got, want := p.render(seg), styles[id].Render(seg); got != want {
					t.Errorf("style %d, segment %q:\n got %q\nwant %q", id, seg, got, want)
				}
			}
		}
	}
}

func TestOpenBrowserRefusesNonWebURLs(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "", "/tmp/x"} {
		if err := openBrowser(u); err == nil {
			t.Errorf("openBrowser(%q) was allowed", u)
		}
	}
}

// columnText pulls one field out of a rendered row.
func columnText(line string, cols []column, k colKind) string {
	r, pos := []rune(line), 0
	for _, c := range cols {
		if c.kind == k {
			return strings.TrimSpace(string(r[min(pos, len(r)):min(pos+c.width, len(r))]))
		}
		pos += c.width
	}
	return ""
}

func TestLiveDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0S"}, {-time.Second, "0S"}, {45 * time.Second, "45S"},
		{time.Minute, "1M"}, {4*time.Minute + 59*time.Second, "4M"},
		{time.Hour, "1H00M"}, {3*time.Hour + 4*time.Minute, "3H04M"},
	}
	for _, c := range cases {
		if got := liveDur(c.d); got != c.want {
			t.Errorf("liveDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	// A finished run keeps its exact duration; only a live one is rounded.
	now := time.Now()
	done := ghapi.Run{Status: "completed", StartedAt: now.Add(-90 * time.Second), UpdatedAt: now}
	if got := ageText(done, now); got != "1M30S" {
		t.Errorf("finished run age = %q, want 1M30S", got)
	}
	live := ghapi.Run{Status: "in_progress", StartedAt: now.Add(-90 * time.Second)}
	if got := ageText(live, now); got != "1M" {
		t.Errorf("live run age = %q, want 1M", got)
	}
}

// A live run's age keeps changing, so its row is retargeted every second.
// The board must still settle. It used to not: the stagger delay was
// measured from the start of the row, so the AGE column spent most of every
// second waiting its turn, was reset before it arrived, and sat there
// crawling one glyph at a time.
func TestLiveRowSettlesAsTimePasses(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	var elapsed time.Duration

	m := New(nil, Options{Interval: time.Minute, MaxRows: 10})
	m.clock = func() time.Time { return base.Add(elapsed) }
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(ghapi.Result{Runs: []ghapi.Run{{
		ID: 1, Repo: "acme/web", Workflow: "ci", Branch: "main", Actor: "octocat",
		Status: "in_progress", StartedAt: base.Add(-4 * time.Minute),
	}}}))

	// Ten seconds of animation-rate frames, with the clock actually moving.
	settledAfter := -1
	for i := range 200 {
		elapsed += animFrame
		m = frame(m)
		if settledAfter < 0 && m.rows[0].fl.settled() {
			settledAfter = i + 1
		}
	}
	if settledAfter < 0 {
		t.Fatalf("a live row never settled in 10s; it shows %q", m.rows[0].fl.String())
	}
	t.Logf("settled after %v", time.Duration(settledAfter)*animFrame)

	cols := columns(110)
	if got := columnText(m.rows[0].fl.String(), cols, colAge); got != "4M" {
		t.Errorf("AGE = %q, want 4M", got)
	}
	if got := columnText(m.rows[0].fl.String(), cols, colRepo); got != "ACME/WEB" {
		t.Errorf("REPO = %q, want ACME/WEB", got)
	}

	// A settled live row lets the board drop out of animation speed; before,
	// it would have repainted at 20 fps for as long as the run was going.
	if got := m.nextFrame(); got <= animFrame {
		t.Errorf("settled live board still schedules %v, want more than %v", got, animFrame)
	}

	// It tracks the next minute over, rather than sticking at 4M.
	for range 1400 { // another 70s
		elapsed += animFrame
		m = frame(m)
	}
	if got := columnText(m.rows[0].fl.String(), cols, colAge); got != "5M" {
		t.Errorf("AGE after a further 70s = %q, want 5M", got)
	}
}
