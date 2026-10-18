package board

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/jzbz/clack/internal/ghapi"
)

// The board's palette: amber on dark, like the real thing.
var (
	bgColor    = lipgloss.Color("#161511")
	selBgColor = lipgloss.Color("#2b2618")
	textColor  = lipgloss.Color("#e8e3d5")
	dimColor   = lipgloss.Color("#6f6a5c")
	amberColor = lipgloss.Color("#ffb000")
	greenColor = lipgloss.Color("#8fd07a")
	redColor   = lipgloss.Color("#f26d6d")
)

// blinkPeriod is how long a BOARDING status stays lit, and then dark.
const blinkPeriod = 600 * time.Millisecond

// styleID names every way the board paints a cell. Each one is flattened to
// a pair of escape sequences on first use (see paint), so drawing a frame
// never constructs a style.
type styleID int

const (
	idBlank styleID = iota // background only, for padding
	idText
	idDim
	idAmber
	idBrand
	idClock
	idBoarding
	idDeparted
	idCancelled
	idError
	numStyleIDs
)

type styleSet [numStyleIDs]lipgloss.Style

func buildStyles(bg lipgloss.Color) styleSet {
	base := lipgloss.NewStyle().Background(bg)
	return styleSet{
		idBlank:     base,
		idText:      base.Foreground(textColor),
		idDim:       base.Foreground(dimColor),
		idAmber:     base.Foreground(amberColor),
		idBrand:     base.Foreground(amberColor).Bold(true),
		idClock:     base.Foreground(textColor).Bold(true),
		idBoarding:  base.Foreground(amberColor).Bold(true),
		idDeparted:  base.Foreground(greenColor).Bold(true),
		idCancelled: base.Foreground(redColor).Bold(true),
		idError:     base.Foreground(redColor),
	}
}

// paint is a style flattened to the pair of escape sequences lipgloss would
// wrap a string in. Style.Render re-runs its width, border, margin and
// grapheme-segmentation machinery on every call, which is most of the cost
// of a frame; the board only ever needs a foreground, a background and bold,
// and those sequences never change. Rendering becomes three string writes.
type paint struct{ on, off string }

func newPaint(st lipgloss.Style) paint {
	const sentinel = "\x00"
	on, off, ok := strings.Cut(st.Render(sentinel), sentinel)
	if !ok {
		return paint{} // a profile with no colour at all
	}
	return paint{on: on, off: off}
}

// write appends s to b wearing this paint.
func (p paint) write(b *strings.Builder, s string) {
	b.WriteString(p.on)
	b.WriteString(s)
	b.WriteString(p.off)
}

func (p paint) render(s string) string { return p.on + s + p.off }

// palette is the board's styles reduced to escape sequences, one set for
// ordinary rows and one for the selected row.
type palette struct {
	plain [numStyleIDs]paint
	sel   [numStyleIDs]paint
}

var (
	paletteOnce sync.Once
	thePalette  palette
)

// paints builds the palette on first use. By then bubbletea has set the
// terminal up, so lipgloss has settled on the colour profile it would have
// used for a direct Render.
func paints() *palette {
	paletteOnce.Do(func() {
		plain, sel := buildStyles(bgColor), buildStyles(selBgColor)
		for i := range thePalette.plain {
			thePalette.plain[i] = newPaint(plain[i])
			thePalette.sel[i] = newPaint(sel[i])
		}
	})
	return &thePalette
}

// row picks the paints for an ordinary or a selected row.
func (p *palette) row(selected bool) *[numStyleIDs]paint {
	if selected {
		return &p.sel
	}
	return &p.plain
}

// status is the departures-board vocabulary a run's state maps onto.
type status int

const (
	statusQueued status = iota
	statusWaiting
	statusBoarding
	statusDeparted
	statusCancelled
	statusSkipped
	statusHold
	statusEnded
	numStatuses
)

var statusWords = [numStatuses]string{
	statusQueued:    "QUEUED",
	statusWaiting:   "WAITING",
	statusBoarding:  "BOARDING",
	statusDeparted:  "DEPARTED",
	statusCancelled: "CANCELLED",
	statusSkipped:   "SKIPPED",
	statusHold:      "HOLD",
	statusEnded:     "ENDED",
}

func (s status) String() string { return statusWords[s] }

// statusOf maps a run's state onto departures-board vocabulary.
func statusOf(r ghapi.Run) status {
	if r.Status != "completed" {
		switch r.Status {
		case "in_progress":
			return statusBoarding
		case "waiting":
			return statusWaiting
		default: // queued, requested, pending
			return statusQueued
		}
	}
	switch r.Conclusion {
	case "success":
		return statusDeparted
	case "skipped":
		return statusSkipped
	case "action_required":
		return statusHold
	case "":
		return statusEnded
	default: // failure, cancelled, timed_out, startup_failure
		return statusCancelled
	}
}

var statusStyle = [numStatuses]styleID{
	statusQueued:    idDim,
	statusWaiting:   idDim,
	statusBoarding:  idBoarding,
	statusDeparted:  idDeparted,
	statusCancelled: idCancelled,
	statusSkipped:   idDim,
	statusHold:      idDim,
	statusEnded:     idDim,
}

type colKind int

const (
	colTime colKind = iota
	colRepo
	colWorkflow
	colBranch
	colActor
	colStatus
	colAge
	numCols
)

// colStyle is how each column is painted; the status column overrides it.
var colStyle = [numCols]styleID{
	colTime:     idDim,
	colRepo:     idAmber,
	colWorkflow: idText,
	colBranch:   idText,
	colActor:    idText,
	colStatus:   idText,
	colAge:      idDim,
}

// column is one board field; width includes a trailing one-cell gap.
type column struct {
	kind  colKind
	title string
	width int
}

// cell renders s as this column: its text, then the gap that keeps it off the
// next column, so a value that exactly fills the field cannot run into its
// neighbour.
func (c column) cell(s string) string { return fit(s, c.width-1) + " " }

// minWorkflow is the narrowest the flexing workflow column may get.
const minWorkflow = 8

// columns lays the board out for a terminal width: the fixed fields shrink in
// tiers, the workflow column takes up the slack, and on a very narrow
// terminal the least essential fields drop off the board altogether.
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
		{colWorkflow, "WORKFLOW", minWorkflow},
		{colBranch, "BRANCH", branch},
		{colActor, "ACTOR", actor},
		{colStatus, "STATUS", 11},
		{colAge, "AGE", 7},
	}
	for _, drop := range []colKind{colActor, colBranch, colTime} {
		if totalWidth(cols) <= width {
			break
		}
		cols = slices.DeleteFunc(cols, func(c column) bool { return c.kind == drop })
	}
	if slack := width - totalWidth(cols); slack > 0 {
		for i := range cols {
			if cols[i].kind == colWorkflow {
				cols[i].width += slack
			}
		}
	}
	return cols
}

func totalWidth(cols []column) int {
	n := 0
	for _, c := range cols {
		n += c.width
	}
	return n
}

// spaces is sliced for padding rather than building a fresh run of blanks.
const maxPad = 512

var spaces = strings.Repeat(" ", maxPad)

func blanks(n int) string {
	switch {
	case n <= 0:
		return ""
	case n <= maxPad:
		return spaces[:n]
	default:
		return strings.Repeat(" ", n)
	}
}

// fit renders s into exactly w flap cells: uppercased, padded or truncated,
// with anything the board has no flap for replaced by unknownGlyph. Every
// cell ends up one terminal column wide, so an emoji in a workflow name
// cannot shove the columns to its right out of line.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(w)
	n := 0
	for _, r := range s {
		if n == w {
			break
		}
		b.WriteRune(glyph(unicode.ToUpper(r)))
		n++
	}
	b.WriteString(blanks(w - n))
	return b.String()
}

func startedClock(r ghapi.Run) string {
	if r.StartedAt.IsZero() {
		return "--:--"
	}
	return r.StartedAt.Local().Format("15:04")
}

// ageOf is the elapsed time of a live run, or the duration of a finished one.
func ageOf(r ghapi.Run, now time.Time) time.Duration {
	if r.Status == "completed" {
		if r.UpdatedAt.After(r.StartedAt) {
			return r.UpdatedAt.Sub(r.StartedAt)
		}
		return 0
	}
	if r.StartedAt.IsZero() {
		return 0
	}
	return now.Sub(r.StartedAt)
}

// ageText is the AGE column: how long a live run has been going, or how
// long a finished one took.
func ageText(r ghapi.Run, now time.Time) string {
	d := ageOf(r, now)
	if r.Status == "completed" {
		return shortDur(d) // settled for good, so spell it out
	}
	return liveDur(d)
}

// liveDur formats an age that is still counting: seconds while the run is
// under a minute old, whole minutes after that. A flap cell takes a second
// or more to turn — a digit rolling 9 to 0 is a lap of the whole ramp — so a
// live seconds counter would spend its life mid-spin, never settling and
// never letting the board stop repainting.
func liveDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dS", max(int64(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dM", int64(d/time.Minute))
	default:
		return fmt.Sprintf("%dH%02dM", int64(d/time.Hour), int64(d/time.Minute)%60)
	}
}

// shortDur formats a duration in compact board style: 45S, 12M05S, 3H04M.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dS", int64(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dM%02dS", int64(d/time.Minute), int64(d/time.Second)%60)
	default:
		return fmt.Sprintf("%dH%02dM", int64(d/time.Hour), int64(d/time.Minute)%60)
	}
}

// rowText renders a run as the flat padded string the flap animates toward.
func rowText(r ghapi.Run, st status, cols []column, now time.Time) string {
	var b strings.Builder
	b.Grow(totalWidth(cols))
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
			s = st.String()
		case colAge:
			s = ageText(r, now)
		}
		b.WriteString(c.cell(s))
	}
	return b.String()
}

// blinkOn reports whether a blinking status is currently lit. It is driven by
// the wall clock rather than a frame counter, so the rhythm stays even
// however often the board repaints.
func (m Model) blinkOn() bool { return (m.now.UnixNano()/int64(blinkPeriod))%2 == 0 }

// renderRow slices the row's current flap text into styled column segments.
func (m Model) renderRow(b *strings.Builder, r *row, cols []column, selected bool) {
	pt := paints().row(selected)
	cur := r.fl.cur
	if len(cur) > m.width {
		cur = cur[:m.width]
	}
	// A BOARDING status blinks off entirely, like a real board.
	dark := r.st == statusBoarding && !m.blinkOn()

	pos := 0
	for _, c := range cols {
		if pos >= len(cur) {
			break
		}
		end := min(pos+c.width, len(cur))
		id := colStyle[c.kind]
		seg := string(cur[pos:end])
		if c.kind == colStatus {
			id = statusStyle[r.st]
			if dark {
				seg = blanks(end - pos)
			}
		}
		pt[id].write(b, seg)
		pos = end
	}
	pt[idBlank].write(b, blanks(m.width-pos))
}

// padTo pads an already-painted line of plain width w out to the board edge.
func padTo(s string, w, width int) string {
	return s + paints().plain[idBlank].render(blanks(width-w))
}

// chromeLines is everything View draws that is not a board row: the header,
// the column header, the rule, the spacer and the footer's status line.
const chromeLines = 5

// View renders the whole board.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	cols := columns(m.width)
	panel := m.panelRows()
	pt := &paints().plain

	var b strings.Builder
	// Every line carries a handful of escape sequences on top of its cells.
	b.Grow((panel + chromeLines + 2) * (m.width + 192))

	b.WriteString(m.renderHeader())
	b.WriteByte('\n')
	b.WriteString(m.renderColHeader(cols))
	b.WriteByte('\n')
	pt[idDim].write(&b, strings.Repeat("─", m.width))

	// The panel keeps a fixed height: unused lines are blank flaps.
	drawn := 0
	for _, r := range m.rows {
		if drawn == panel {
			break
		}
		b.WriteByte('\n')
		m.renderRow(&b, r, cols, r.id == m.selID)
		drawn++
	}
	if drawn == 0 {
		msg := "  NO FLIGHTS ON THE BOARD"
		if m.polling {
			msg = "  WAITING FOR FIRST POLL…"
		}
		b.WriteByte('\n')
		b.WriteString(padTo(pt[idDim].render(msg), utf8.RuneCountInString(msg), m.width))
		drawn = 1
	}
	for ; drawn < panel; drawn++ {
		b.WriteByte('\n')
		pt[idBlank].write(&b, blanks(m.width))
	}

	// The spacer is painted too, so the board reads as one solid panel.
	b.WriteByte('\n')
	pt[idBlank].write(&b, blanks(m.width))
	b.WriteByte('\n')
	b.WriteString(m.renderFooter())
	return b.String()
}

// panelRows is how many board rows fit between the header and the footer.
func (m Model) panelRows() int {
	chrome := chromeLines
	if m.err != nil {
		chrome++
	}
	if m.showHelp {
		chrome++
	}
	panel := m.maxRows
	if limit := m.height - chrome; m.height > 0 && limit < panel {
		panel = limit
	}
	return max(panel, 1)
}

const (
	brand   = "CLACK"
	tagline = " · GITHUB ACTIONS DEPARTURES"
)

var taglineWidth = utf8.RuneCountInString(tagline)

func (m Model) renderHeader() string {
	pt := &paints().plain
	clock := m.now.Format("15:04:05")

	var b strings.Builder
	pt[idBrand].write(&b, brand)
	used := len(brand)
	if used+taglineWidth+len(clock) < m.width {
		pt[idDim].write(&b, tagline)
		used += taglineWidth
	}
	pt[idBlank].write(&b, blanks(max(m.width-used-len(clock), 1)))
	pt[idClock].write(&b, clock)
	return b.String()
}

func (m Model) renderColHeader(cols []column) string {
	pt := &paints().plain
	var b strings.Builder
	pos := 0
	for _, c := range cols {
		if pos+c.width > m.width {
			break
		}
		pt[idDim].write(&b, c.cell(c.title))
		pos += c.width
	}
	return padTo(b.String(), pos, m.width)
}

func (m Model) renderFooter() string {
	pt := &paints().plain
	var b strings.Builder
	if m.err != nil {
		line := clampRunes(oneLine(m.err), m.width)
		b.WriteString(padTo(pt[idError].render(line), utf8.RuneCountInString(line), m.width))
		b.WriteByte('\n')
	}
	if m.showHelp {
		const help = "↑/K ↓/J SELECT · G/SHIFT+G TOP/BOTTOM · O/ENTER OPEN RUN · R REFRESH · ? CLOSE · Q QUIT"
		line := clampRunes(help, m.width)
		b.WriteString(padTo(pt[idDim].render(line), utf8.RuneCountInString(line), m.width))
		b.WriteByte('\n')
	}

	left := m.statusLine()
	const right = "O OPEN · ? HELP · Q QUIT"
	left = clampRunes(left, max(m.width-utf8.RuneCountInString(right)-1, 0))
	gap := max(m.width-utf8.RuneCountInString(left)-utf8.RuneCountInString(right), 1)

	pt[idDim].write(&b, left)
	pt[idBlank].write(&b, blanks(gap))
	pt[idDim].write(&b, right)
	return b.String()
}

// statusLine is the footer's left half: polling cadence and API budget.
func (m Model) statusLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "POLL %s", m.interval)
	if m.rl.Remaining >= 0 {
		fmt.Fprintf(&b, " · API %d LEFT", m.rl.Remaining)
	}
	if m.rl.Remaining == 0 && m.rl.Reset.After(m.now) {
		b.WriteString(" · RATE LIMITED UNTIL " + m.rl.Reset.Local().Format("15:04"))
	}
	switch {
	case m.polling:
		b.WriteString(" · POLLING…")
	case !m.nextPollAt.IsZero():
		b.WriteString(" · NEXT " + shortDur(m.nextPollAt.Sub(m.now)))
	}
	return b.String()
}

// oneLine flattens an error onto the footer's single line. errors.Join
// separates its parts with newlines, which would otherwise tear the frame.
func oneLine(err error) string {
	return "! " + strings.Join(strings.FieldsFunc(err.Error(), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

// clampRunes truncates s to at most w cells.
func clampRunes(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	n := 0
	for i := range s {
		if n == w {
			return s[:i]
		}
		n++
	}
	return s
}
