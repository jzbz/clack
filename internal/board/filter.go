package board

import (
	"strings"

	"github.com/jzbz/clack/internal/ghapi"
)

// filterMode is the preset view the board is showing.
type filterMode int

const (
	showAll filterMode = iota
	showLive
	showFailed
	numFilterModes
)

var filterNames = [numFilterModes]string{
	showAll:    "ALL",
	showLive:   "LIVE",
	showFailed: "FAILED",
}

func (f filterMode) String() string { return filterNames[f] }

func (f filterMode) next() filterMode { return (f + 1) % numFilterModes }

// filter narrows what the board shows: one of the preset views, and free
// text matched against the fields a row puts on screen.
type filter struct {
	mode  filterMode
	query string // as typed, for the footer
	fold  string // lowercased, for matching
}

// setQuery records a query and the folded form matching uses.
func (f *filter) setQuery(q string) {
	f.query, f.fold = q, strings.ToLower(q)
}

// active reports whether the filter is hiding anything.
func (f filter) active() bool { return f.mode != showAll || f.query != "" }

// clear puts the whole board back on screen.
func (f *filter) clear() { *f = filter{} }

// match reports whether a run belongs on the filtered board.
//
// FAILED is broader than what rings the bell: a run somebody cancelled is
// not worth interrupting them for, but it is certainly worth seeing when
// they ask the board what went wrong.
func (f filter) match(r ghapi.Run, st status) bool {
	switch f.mode {
	case showLive:
		if r.Status == "completed" {
			return false
		}
	case showFailed:
		if st != statusCancelled && st != statusHold {
			return false
		}
	}
	return f.fold == "" || matchesText(r, f.fold)
}

// matchesText looks for fold, which is already lowercased, in the fields the
// board displays.
func matchesText(r ghapi.Run, fold string) bool {
	for _, s := range [...]string{r.Repo, r.Workflow, r.Branch, r.Actor} {
		if strings.Contains(strings.ToLower(s), fold) {
			return true
		}
	}
	return false
}
