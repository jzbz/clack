// Package board is the bubbletea model and rendering for the departures board.
package board

import (
	"cmp"
	"context"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzbz/clack/internal/ghapi"
)

// Fetcher polls every watched repo and returns the combined result.
type Fetcher func(ctx context.Context) ghapi.Result

const (
	animFrame    = 50 * time.Millisecond // repaint rate while flaps are moving
	minFrame     = 10 * time.Millisecond // floor on a scheduled repaint
	idleFrame    = time.Second           // a settled board still ticks its clock
	minInterval  = 10 * time.Second
	maxBackoff   = 5 * time.Minute
	fetchTimeout = 30 * time.Second
)

// Model is the bubbletea model for the board.
type Model struct {
	fetch    Fetcher
	interval time.Duration
	maxRows  int

	rows       []*row // every run the last poll reported, in board order
	shown      []*row // the subset the filter lets through
	top        int    // index into shown of the first row on screen
	byID       map[int64]*row
	selID      int64 // run ID of the highlighted row; 0 = none yet
	filter     filter
	typing     bool   // the query line has the keyboard
	queryWas   string // query to restore if the user escapes out
	polling    bool
	nextPollAt time.Time
	rl         ghapi.RateLimit
	err        error
	failures   int
	width      int
	height     int
	showHelp   bool

	now      time.Time // wall clock as of the current message; View reads only this
	frame    int       // generation of the live frame clock
	lastText int64     // unix second the row text was last recomputed

	alerted map[int64]bool // runs already reported as failed
	seeded  bool           // a first poll has landed, so changes are news

	// clock overrides the wall clock in tests, where the board has to be
	// driven through real elapsed time to show how it behaves.
	clock func() time.Time

	// notify delivers failures. nil when the user wants no alerts, and
	// replaced in tests so they neither ring nor spawn anything.
	notify notifier
}

func (m Model) timeNow() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// row is one board line, keyed by run ID so edits flap in place.
type row struct {
	id  int64
	run ghapi.Run
	st  status
	fl  flap
}

// Options configure the board beyond where its runs come from.
type Options struct {
	Interval time.Duration // poll interval, floored at minInterval
	MaxRows  int           // most runs to keep on the board
	Bell     bool          // ring the terminal when a run fails
	OnFail   string        // shell command to run when a run fails
}

// New builds a Model polling with fetch.
func New(fetch Fetcher, opt Options) Model {
	return Model{
		fetch:    fetch,
		interval: max(opt.Interval, minInterval),
		maxRows:  max(opt.MaxRows, 1),
		byID:     map[int64]*row{},
		alerted:  map[int64]bool{},
		rl:       ghapi.RateLimit{Remaining: -1},
		now:      time.Now(),
		notify:   newNotifier(opt.Bell, opt.OnFail),
	}
}

type (
	pollMsg    struct{}
	frameMsg   struct{ gen int } // gen fences out ticks from a superseded schedule
	runsMsg    ghapi.Result
	browserMsg struct{ err error }
	alertMsg   struct{ err error }
)

// Init starts the first poll and the frame clock.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), frameIn(animFrame, m.frame), tea.SetWindowTitle("clack"))
}

func frameIn(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return frameMsg{gen} })
}

// nextFrameCmd schedules the next repaint in d and supersedes any frame tick
// already in flight, so a fresh animation is never left running off an idle
// timer. Every generation bump must come with a new tick or the clock stops.
func (m *Model) nextFrameCmd(d time.Duration) tea.Cmd {
	m.frame++
	return frameIn(d, m.frame)
}

// nextFrame is how long the board may stand still: until a flap moves, a
// BOARDING status blinks, or the clock ticks over to the next second. A
// settled board therefore repaints once a second instead of twenty times.
func (m Model) nextFrame() time.Duration {
	boarding := false
	for _, r := range m.window() {
		if !r.fl.settled() {
			return animFrame
		}
		boarding = boarding || r.st == statusBoarding
	}
	since := time.Duration(m.now.UnixNano())
	d := idleFrame - since%idleFrame
	if boarding {
		d = min(d, blinkPeriod-since%blinkPeriod)
	}
	return max(d, minFrame)
}

func (m Model) fetchCmd() tea.Cmd {
	fetch := m.fetch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return runsMsg(fetch(ctx))
	}
}

// Update handles keys, poll results and the frame clock.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.now = m.timeNow()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.retarget()
		m.scrollToSelection()
		cmd := m.nextFrameCmd(animFrame)
		return m, cmd

	case tea.KeyMsg:
		if m.typing {
			return m.handleQueryKey(msg)
		}
		return m.handleKey(msg)

	case browserMsg:
		if msg.err != nil {
			m.err = msg.err
		}

	case alertMsg:
		if msg.err != nil {
			m.err = msg.err
		}

	case pollMsg:
		if !m.polling {
			m.polling = true
			return m, m.fetchCmd()
		}

	case runsMsg:
		m.polling = false
		res := ghapi.Result(msg)
		if res.Err != nil {
			m.err = res.Err
			m.failures++
		} else {
			m.err = nil
			m.failures = 0
		}
		m.rl = res.RateLimit
		// A clean poll with no runs really does mean an empty board; a
		// failed one keeps whatever was last known good.
		var failed []ghapi.Run
		if res.Err == nil || len(res.Runs) > 0 {
			failed = m.merge(res.Runs)
		}
		delay := m.nextPollIn()
		m.nextPollAt = m.now.Add(delay)
		poll := tea.Tick(delay, func(time.Time) tea.Msg { return pollMsg{} })
		return m, tea.Batch(poll, m.nextFrameCmd(m.nextFrame()), m.alertCmd(failed))

	case frameMsg:
		if msg.gen != m.frame {
			return m, nil // superseded schedule
		}
		// Only what is on screen animates; a row scrolled out of view
		// costs nothing and flaps in when it returns.
		for _, r := range m.window() {
			r.fl.step()
		}
		// Ages count live, so recompute the row text once a second.
		// Unchanged text is a no-op; a changed digit flaps over.
		if sec := m.now.Unix(); sec != m.lastText {
			m.lastText = sec
			m.retarget()
		}
		cmd := m.nextFrameCmd(m.nextFrame())
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		// Escape backs out of a filter first; it only quits a board that
		// is already showing everything.
		if !m.filter.active() {
			return m, tea.Quit
		}
		m.filter.clear()
		m.refilter()
	case "f":
		m.filter.mode = m.filter.mode.next()
		m.refilter()
	case "/":
		m.typing, m.queryWas = true, m.filter.query
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.moveTo(0)
	case "G", "end":
		m.moveTo(len(m.shown) - 1)
	case "pgdown", "ctrl+f":
		m.move(m.panelRows())
	case "pgup", "ctrl+b":
		m.move(-m.panelRows())
	case "o", "enter":
		if r := m.selected(); r != nil && r.run.URL != "" {
			url := r.run.URL
			return m, func() tea.Msg { return browserMsg{openBrowser(url)} }
		}
	case "?":
		m.showHelp = !m.showHelp
	case "r":
		if !m.polling {
			m.polling = true
			return m, m.fetchCmd()
		}
	}
	return m, nil
}

// handleQueryKey edits the search while the query line has the keyboard.
func (m Model) handleQueryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEnter:
		m.typing = false // keep what was typed
	case tea.KeyEsc:
		m.typing = false
		m.filter.setQuery(m.queryWas)
		m.refilter()
	case tea.KeyBackspace:
		if q := []rune(m.filter.query); len(q) > 0 {
			m.filter.setQuery(string(q[:len(q)-1]))
			m.refilter()
		}
	case tea.KeyCtrlU:
		m.filter.setQuery("")
		m.refilter()
	default:
		// Space arrives as its own key type but still carries its rune,
		// so one check covers every printable key. Alt-chords are not
		// text and must not land in the query.
		if len(msg.Runes) > 0 && !msg.Alt {
			m.filter.setQuery(m.filter.query + string(msg.Runes))
			m.refilter()
		}
	}
	return m, nil
}

// nextPollIn backs off exponentially on failures and waits out a spent rate
// limit instead of hammering the API.
func (m Model) nextPollIn() time.Duration {
	if m.rl.Remaining == 0 {
		if d := m.rl.Reset.Sub(m.now) + 2*time.Second; d > 0 {
			return d
		}
	}
	d := m.interval
	for i := 0; i < m.failures && i < 4; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// merge folds freshly fetched runs into the board: sort, trim, then retarget
// each surviving row's flap so changes animate. It returns the runs that
// have just gone wrong, which the caller turns into an alert.
func (m *Model) merge(runs []ghapi.Run) []ghapi.Run {
	failed := m.newFailures(runs)
	sortRuns(runs)
	cols := columns(m.width)
	next := make([]*row, 0, len(runs))
	for _, rn := range runs {
		r, ok := m.byID[rn.ID]
		if !ok {
			r = &row{id: rn.ID}
		}
		r.run, r.st = rn, statusOf(rn)
		r.fl.setTarget(rowText(rn, r.st, cols, m.now))
		next = append(next, r)
	}
	// Rebuild the index so runs that fell off the board are forgotten.
	clear(m.byID)
	for _, r := range next {
		m.byID[r.id] = r
	}
	m.rows = next
	m.refilter()
	return failed
}

// refilter recomputes which rows are on screen. Everything the poll
// returned stays in m.rows: the filter is a view over it, so changing the
// filter never needs another API call.
func (m *Model) refilter() {
	m.shown = m.shown[:0]
	for _, r := range m.rows {
		if m.filter.match(r.run, r.st) {
			m.shown = append(m.shown, r)
		}
	}
	m.fixSelection()
	m.scrollToSelection()
}

// window is the slice of filtered rows actually on screen.
func (m Model) window() []*row {
	panel := m.panelRows()
	top := max(0, min(m.top, len(m.shown)-panel))
	return m.shown[top:min(top+panel, len(m.shown))]
}

// scrollToSelection scrolls the least it can to keep the selection in view.
func (m *Model) scrollToSelection() {
	panel := m.panelRows()
	i := m.selectedIndex()
	m.top = min(m.top, i)
	m.top = max(m.top, i-panel+1)
	m.top = max(0, min(m.top, max(len(m.shown)-panel, 0)))
}

// newFailures picks out the runs that have broken since the last poll, given
// everything this poll reported — before the board is trimmed to maxRows, so
// a failure pushed off the bottom still gets its one alert.
//
// Nothing is reported for the first poll: the board is seeding itself from
// history there, and every red run already on it is old news.
func (m *Model) newFailures(runs []ghapi.Run) []ghapi.Run {
	var failed []ghapi.Run
	seen := make(map[int64]bool, len(runs))
	for _, r := range runs {
		seen[r.ID] = true
		if !isFailure(r) || m.alerted[r.ID] {
			continue
		}
		m.alerted[r.ID] = true
		if m.seeded {
			failed = append(failed, r)
		}
	}
	// Forget runs GitHub no longer reports. That keeps the set bounded, and
	// a run still in the window is never alerted on twice, however often it
	// drops off the visible board and comes back.
	for id := range m.alerted {
		if !seen[id] {
			delete(m.alerted, id)
		}
	}
	m.seeded = true
	return failed
}

// alertCmd delivers failures off the update loop, so a slow hook cannot
// stall the board.
func (m Model) alertCmd(failed []ghapi.Run) tea.Cmd {
	notify := m.notify
	if notify == nil || len(failed) == 0 {
		return nil
	}
	return func() tea.Msg { return alertMsg{notify(failed)} }
}

// retarget recomputes every row's target text, after a resize or as ages tick.
func (m *Model) retarget() {
	cols := columns(m.width)
	for _, r := range m.rows {
		r.fl.setTarget(rowText(r.run, r.st, cols, m.now))
	}
}

// move shifts the selection by d rows, clamped to the board.
func (m *Model) move(d int) { m.moveTo(m.selectedIndex() + d) }

// moveTo selects the i'th visible row, clamped to the board, and scrolls
// far enough to show it.
func (m *Model) moveTo(i int) {
	if len(m.shown) == 0 {
		return
	}
	m.selID = m.shown[min(max(i, 0), len(m.shown)-1)].id
	m.scrollToSelection()
}

// selectedIndex resolves selID to a visible row index, defaulting to the
// top row.
func (m Model) selectedIndex() int {
	for i, r := range m.shown {
		if r.id == m.selID {
			return i
		}
	}
	return 0
}

// selected returns the highlighted row, or nil when nothing is on screen.
func (m Model) selected() *row {
	if len(m.shown) == 0 {
		return nil
	}
	return m.shown[m.selectedIndex()]
}

// fixSelection keeps the selection on a row that is still showing, after a
// merge or a change of filter.
func (m *Model) fixSelection() {
	if len(m.shown) == 0 {
		m.selID = 0
		return
	}
	for _, r := range m.shown {
		if r.id == m.selID {
			return
		}
	}
	m.selID = m.shown[0].id
}

// sortRuns orders the board: live runs first, then queued, then finished,
// most recent activity first within each group.
func sortRuns(runs []ghapi.Run) {
	slices.SortStableFunc(runs, func(a, b ghapi.Run) int {
		if c := cmp.Compare(runGroup(a), runGroup(b)); c != 0 {
			return c
		}
		return activity(b).Compare(activity(a))
	})
}

func runGroup(r ghapi.Run) int {
	switch r.Status {
	case "in_progress":
		return 0
	case "completed":
		return 2
	default: // queued, requested, waiting, pending
		return 1
	}
}

func activity(r ghapi.Run) time.Time {
	if !r.StartedAt.IsZero() {
		return r.StartedAt
	}
	return r.UpdatedAt
}
