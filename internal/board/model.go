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

	rows       []*row
	byID       map[int64]*row
	selID      int64 // run ID of the highlighted row; 0 = none yet
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
	for _, r := range m.rows {
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
		cmd := m.nextFrameCmd(animFrame)
		return m, cmd

	case tea.KeyMsg:
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
		for _, r := range m.rows {
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
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.moveTo(0)
	case "G", "end":
		m.moveTo(len(m.rows) - 1)
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
	if len(runs) > m.maxRows {
		runs = runs[:m.maxRows]
	}
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
	m.fixSelection()
	return failed
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

// moveTo selects row i, clamped to the board.
func (m *Model) moveTo(i int) {
	if len(m.rows) == 0 {
		return
	}
	m.selID = m.rows[min(max(i, 0), len(m.rows)-1)].id
}

// selectedIndex resolves selID to a row index, defaulting to the top row.
func (m Model) selectedIndex() int {
	for i, r := range m.rows {
		if r.id == m.selID {
			return i
		}
	}
	return 0
}

// selected returns the highlighted row, or nil when the board is empty.
func (m Model) selected() *row {
	if len(m.rows) == 0 {
		return nil
	}
	return m.rows[m.selectedIndex()]
}

// fixSelection keeps the selection on a surviving row after a merge.
func (m *Model) fixSelection() {
	if len(m.rows) == 0 {
		m.selID = 0
		return
	}
	if _, ok := m.byID[m.selID]; !ok {
		m.selID = m.rows[0].id
	}
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
