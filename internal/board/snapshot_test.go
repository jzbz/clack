package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// stripANSI reduces a rendered frame to the characters a terminal shows.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// TestSnapshot renders real board frames into $CLACK_SNAPSHOT so the output
// can be inspected without a terminal. Skipped unless the var is set.
func TestSnapshot(t *testing.T) {
	dir := os.Getenv("CLACK_SNAPSHOT")
	if dir == "" {
		t.Skip("set CLACK_SNAPSHOT to a directory to write rendered frames")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	fetch := Demo()
	m := New(fetch, Options{Interval: 2 * time.Second, MaxRows: 12})
	write := func(name string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(stripANSI(m.View())), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}

	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m = step(m, runsMsg(fetch(t.Context())))
	m = frames(m, 10)
	write("frame-mid.txt")
	m = frames(m, 400)
	write("frame-settled.txt")

	// Advance far enough that queued runs board and boarding runs depart.
	for range 8 {
		m = step(m, runsMsg(fetch(t.Context())))
		m = frames(m, 300)
	}
	write("frame-later.txt")

	// Narrow terminals drop columns rather than running off the edge.
	for _, w := range []int{90, 70, 50} {
		m = step(m, tea.WindowSizeMsg{Width: w, Height: 24})
		m = frames(m, 400)
		write(fmt.Sprintf("frame-w%d.txt", w))
	}

	// Filtered views, and the search as it is typed.
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 24})
	m.filter.mode = showFailed
	m.refilter()
	m = frames(m, 400)
	write("frame-failed.txt")

	m.filter.clear()
	m.filter.setQuery("web")
	m.typing = true
	m.refilter()
	m = frames(m, 400)
	write("frame-search.txt")
	m.typing = false

	// The help line and an error share the footer with the status line.
	m = step(m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m.showHelp = true
	m.err = errors.New("acme/ghost: 404 Not Found")
	m = frames(m, 400)
	write("frame-help.txt")
}
