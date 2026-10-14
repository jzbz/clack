package board

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

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
	m := New(fetch, 2*time.Second, 12)
	step := func(msg tea.Msg) {
		um, _ := m.Update(msg)
		m = um.(Model)
	}
	flaps := func(n int) {
		for i := 0; i < n; i++ {
			step(flapTickMsg{})
		}
	}
	write := func(name string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(ansiRe.ReplaceAllString(m.View(), "")), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	step(tea.WindowSizeMsg{Width: 110, Height: 30})
	res := fetch(context.Background())
	step(runsMsg(res))
	flaps(10)
	write("frame-mid.txt")
	flaps(400)
	write("frame-settled.txt")

	// Advance far enough that queued runs board and boarding runs depart.
	for p := 0; p < 8; p++ {
		step(runsMsg(fetch(context.Background())))
		flaps(300)
	}
	write("frame-later.txt")
}
