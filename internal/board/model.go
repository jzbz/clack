// Package board is the bubbletea model and rendering for the departures board.
package board

import (
	"context"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"clack/internal/ghapi"
)

// Fetcher polls every watched repo and returns the combined result.
type Fetcher func(ctx context.Context) ghapi.Result

const (
	flapInterval = 50 * time.Millisecond
	maxBackoff   = 5 * time.Minute
)

// Model is the bubbletea model for the board.
type Model struct {
	fetch    Fetcher
	interval time.Duration
	maxRows  int

	rows     []*row
	byID     map[int64]*row
	polling  bool
	lastPoll time.Time
	rl       ghapi.RateLimit
	err      error
	failures int
	tick     int64
	width    int
	height   int
}

// row is one board line, keyed by run ID so edits flap in place.
type row struct {
	id  int64
	run ghapi.Run
	fl  flap
}

// New builds a Model polling with fetch every interval, showing maxRows runs.
func New(fetch Fetcher, interval time.Duration, maxRows int) Model {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	if maxRows < 1 {
		maxRows = 1
	}
	return Model{
		fetch:    fetch,
		interval: interval,
		maxRows:  maxRows,
		byID:     map[int64]*row{},
		rl:       ghapi.RateLimit{Remaining: -1},
	}
}

type pollTickMsg struct{}
type flapTickMsg struct{}
type runsMsg ghapi.Result

// Init starts the first poll and the animation clock.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), tickFlap())
}

func tickFlap() tea.Cmd {
	return tea.Tick(flapInterval, func(time.Time) tea.Msg { return flapTickMsg{} })
}

func (m Model) fetchCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return runsMsg(m.fetch(ctx))
	}
}

// Update handles keys, poll results and the animation clock.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.retarget()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			if !m.polling {
				m.polling = true
				return m, m.fetchCmd()
			}
		}
	case pollTickMsg:
		if !m.polling {
			m.polling = true
			return m, m.fetchCmd()
		}
	case runsMsg:
		m.polling = false
		m.lastPoll = time.Now()
		res := ghapi.Result(msg)
		if res.Err != nil {
			m.err = res.Err
			m.failures++
		} else {
			m.err = nil
			m.failures = 0
		}
		m.rl = res.RateLimit
		if len(res.Runs) > 0 {
			m.merge(res.Runs)
		}
		return m, tea.Tick(m.nextPollIn(), func(time.Time) tea.Msg { return pollTickMsg{} })
	case flapTickMsg:
		m.tick++
		for _, r := range m.rows {
			r.fl.step()
		}
		// Once a second, recompute row text so live ages keep counting.
		// Unchanged text is a no-op; a changed digit flaps over.
		if m.tick%20 == 0 {
			m.retarget()
		}
		return m, tickFlap()
	}
	return m, nil
}

// nextPollIn backs off exponentially on failures and waits out a spent rate
// limit instead of hammering the API.
func (m Model) nextPollIn() time.Duration {
	if m.rl.Remaining == 0 {
		if d := time.Until(m.rl.Reset) + 2*time.Second; d > 0 {
			return d
		}
	}
	d := m.interval
	for i := 0; i < m.failures && i < 4; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// merge folds freshly fetched runs into the board: sort, trim, then retarget
// each surviving row's flap so changes animate.
func (m *Model) merge(runs []ghapi.Run) {
	sortRuns(runs)
	if len(runs) > m.maxRows {
		runs = runs[:m.maxRows]
	}
	cols := columns(m.width)
	next := make([]*row, 0, len(runs))
	seen := make(map[int64]bool, len(runs))
	for _, rn := range runs {
		seen[rn.ID] = true
		r, ok := m.byID[rn.ID]
		if !ok {
			r = &row{id: rn.ID}
			m.byID[rn.ID] = r
		}
		r.run = rn
		r.fl.setTarget(rowText(rn, cols))
		next = append(next, r)
	}
	for id := range m.byID {
		if !seen[id] {
			delete(m.byID, id)
		}
	}
	m.rows = next
}

// retarget recomputes every row's target text after a resize.
func (m *Model) retarget() {
	cols := columns(m.width)
	for _, r := range m.rows {
		r.fl.setTarget(rowText(r.run, cols))
	}
}

// sortRuns orders the board: live runs first, then queued, then finished,
// most recent activity first within each group.
func sortRuns(runs []ghapi.Run) {
	sort.SliceStable(runs, func(i, j int) bool {
		gi, gj := runGroup(runs[i]), runGroup(runs[j])
		if gi != gj {
			return gi < gj
		}
		return activity(runs[i]).After(activity(runs[j]))
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
