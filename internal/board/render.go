package board

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"clack/internal/ghapi"
)

var (
	bgColor    = lipgloss.Color("#14141a")
	textColor  = lipgloss.Color("#e8e3d5")
	dimColor   = lipgloss.Color("#6f6a5c")
	amberColor = lipgloss.Color("#ffb000")
	greenColor = lipgloss.Color("#8fd07a")
	redColor   = lipgloss.Color("#f26d6d")
)

type colKind int

const (
	colTime colKind = iota
	colRepo
	colWorkflow
	colBranch
	colActor
	colStatus
	colAge
)

// column is one board field; width includes a trailing one-cell gap.
type column struct {
	kind  colKind
	title string
	width int
}

// columns lays out the board for a terminal width, flexing the workflow
// column and shrinking the rest on narrow terminals.
func columns(width int) []column {
	repo, branch, actor := 26, 18, 13
	switch {
	case width >= 110:
	case width >= 90:
		repo, branch, actor = 20, 14, 11
	default:
		repo, branch, actor = 14, 10, 9
	}
	cols := []column{
		{colTime, "TIME", 6},
		{colRepo, "REPO", repo},
		{colWorkflow, "WORKFLOW", 0},
		{colBranch, "BRANCH", branch},
		{colActor, "ACTOR", actor},
		{colStatus, "STATUS", 11},
		{colAge, "AGE", 7},
	}
	fixed := 0
	for _, c := range cols {
		fixed += c.width
	}
	cols[2].width = max(8, width-fixed)
	return cols
}

// fit uppercases s and pads or truncates it to exactly w cells.
func fit(s string, w int) string {
	r := []rune(strings.ToUpper(s))
	if len(r) > w {
		r = r[:w]
	}
	return string(r) + strings.Repeat(" ", w-len(r))
}

// statusWord maps a run's state onto departures-board vocabulary.
func statusWord(r ghapi.Run) string {
	if r.Status == "completed" {
		switch r.Conclusion {
		case "success":
			return "DEPARTED"
		case "skipped":
			return "SKIPPED"
		case "action_required":
			return "HOLD"
		case "":
			return "ENDED"
		default: // failure, cancelled, timed_out, startup_failure
			return "CANCELLED"
		}
	}
	switch r.Status {
	case "in_progress":
		return "BOARDING"
	case "waiting":
		return "WAITING"
	default: // queued, requested, pending
		return "QUEUED"
	}
}

func startedClock(r ghapi.Run) string {
	if r.StartedAt.IsZero() {
		return "--:--"
	}
	return r.StartedAt.Local().Format("15:04")
}

// ageOf is the elapsed time of a live run, or the duration of a finished one.
func ageOf(r ghapi.Run) time.Duration {
	if r.Status == "completed" {
		if r.UpdatedAt.After(r.StartedAt) {
			return r.UpdatedAt.Sub(r.StartedAt)
		}
		return 0
	}
	if r.StartedAt.IsZero() {
		return 0
	}
	return time.Since(r.StartedAt)
}

// shortDur formats a duration in compact board style: 45S, 12M, 3H04M.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dS", int64(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dM", int64(d/time.Minute))
	default:
		return fmt.Sprintf("%dH%02dM", int64(d/time.Hour), int64(d/time.Minute)%60)
	}
}

// rowText renders a run as the flat padded string the flap animates toward.
func rowText(r ghapi.Run, cols []column) string {
	var b strings.Builder
	for _, c := range cols {
		var s string
		switch c.kind {
		case colTime:
			s = startedClock(r)
		case colRepo:
			s = r.Repo
		case colWorkflow:
			s = r.Workflow
		case colBranch:
			s = r.Branch
		case colActor:
			s = r.Actor
		case colStatus:
			s = statusWord(r)
		case colAge:
			s = shortDur(ageOf(r))
		}
		b.WriteString(fit(s, c.width))
	}
	return b.String()
}

func styleFor(k colKind, r ghapi.Run, blinkOn bool) lipgloss.Style {
	base := lipgloss.NewStyle().Background(bgColor).Foreground(textColor)
	switch k {
	case colTime, colAge:
		return base.Foreground(dimColor)
	case colRepo:
		return base.Foreground(amberColor)
	case colStatus:
		switch statusWord(r) {
		case "DEPARTED":
			return base.Foreground(greenColor).Bold(true)
		case "BOARDING":
			if blinkOn {
				return base.Foreground(amberColor).Bold(true)
			}
			return base.Foreground(amberColor)
		case "CANCELLED":
			return base.Foreground(redColor).Bold(true)
		default:
			return base.Foreground(dimColor)
		}
	default:
		return base
	}
}

// renderRow slices the row's current flap text into styled column segments.
func (m Model) renderRow(r *row, cols []column) string {
	cur := r.fl.cur
	if m.width > 0 && len(cur) > m.width {
		cur = cur[:m.width]
	}
	blinkOn := (m.tick/10)%2 == 0
	var b strings.Builder
	pos := 0
	for _, c := range cols {
		if pos >= len(cur) {
			break
		}
		end := min(pos+c.width, len(cur))
		b.WriteString(styleFor(c.kind, r.run, blinkOn).Render(string(cur[pos:end])))
		pos = end
	}
	if pad := m.width - pos; pad > 0 {
		b.WriteString(lipgloss.NewStyle().Background(bgColor).Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

func padLine(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		s += lipgloss.NewStyle().Background(bgColor).Render(strings.Repeat(" ", n))
	}
	return s
}

// View renders the whole board.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	cols := columns(m.width)
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderColHeader(cols))

	rows := m.rows
	if limit := m.height - 6; limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		b.WriteString("\n")
		msg := "  NO FLIGHTS ON THE BOARD"
		if m.polling {
			msg = "  WAITING FOR FIRST POLL…"
		}
		b.WriteString(padLine(lipgloss.NewStyle().Background(bgColor).Foreground(dimColor).Render(msg), m.width))
	}
	for _, r := range rows {
		b.WriteString("\n")
		b.WriteString(m.renderRow(r, cols))
	}

	b.WriteString("\n\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderHeader() string {
	title := lipgloss.NewStyle().Background(bgColor).Foreground(amberColor).Bold(true).
		Render("CLACK · GITHUB ACTIONS DEPARTURES")
	clock := lipgloss.NewStyle().Background(bgColor).Foreground(textColor).Bold(true).
		Render(time.Now().Format("15:04:05"))
	gap := max(m.width-lipgloss.Width(title)-lipgloss.Width(clock), 1)
	fill := lipgloss.NewStyle().Background(bgColor).Render(strings.Repeat(" ", gap))
	return title + fill + clock
}

func (m Model) renderColHeader(cols []column) string {
	st := lipgloss.NewStyle().Background(bgColor).Foreground(dimColor)
	var b strings.Builder
	for _, c := range cols {
		if lipgloss.Width(b.String())+c.width > m.width {
			break
		}
		b.WriteString(st.Render(fit(c.title, c.width)))
	}
	return padLine(b.String(), m.width)
}

func (m Model) renderFooter() string {
	var b strings.Builder
	if m.err != nil {
		errLine := fmt.Sprintf("! %v", m.err)
		if r := []rune(errLine); len(r) > m.width {
			errLine = string(r[:m.width])
		}
		b.WriteString(padLine(lipgloss.NewStyle().Background(bgColor).Foreground(redColor).Render(errLine), m.width))
		b.WriteString("\n")
	}

	left := fmt.Sprintf("POLL %s", m.interval)
	if m.rl.Remaining >= 0 {
		left += fmt.Sprintf(" · API %d LEFT", m.rl.Remaining)
	}
	if m.rl.Remaining == 0 && time.Until(m.rl.Reset) > 0 {
		left += " · RATE LIMITED UNTIL " + m.rl.Reset.Local().Format("15:04")
	}
	if m.polling {
		left += " · POLLING…"
	} else if !m.lastPoll.IsZero() {
		left += " · UPDATED " + m.lastPoll.Format("15:04:05")
	}
	right := "R REFRESH · Q QUIT"

	leftSt := lipgloss.NewStyle().Background(bgColor).Foreground(dimColor).Render(left)
	rightSt := lipgloss.NewStyle().Background(bgColor).Foreground(dimColor).Render(right)
	gap := max(m.width-lipgloss.Width(leftSt)-lipgloss.Width(rightSt), 1)
	fill := lipgloss.NewStyle().Background(bgColor).Render(strings.Repeat(" ", gap))
	b.WriteString(leftSt + fill + rightSt)
	return b.String()
}
