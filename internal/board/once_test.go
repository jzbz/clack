package board

import (
	"strings"
	"testing"
	"time"
)

func onceBoard(t *testing.T, opt Options) string {
	t.Helper()
	if opt.Interval == 0 {
		opt.Interval = time.Minute
	}
	if opt.MaxRows == 0 {
		opt.MaxRows = 12
	}
	out, err := Once(t.Context(), Demo(), opt, 96)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	return out
}

// A printed board is read, not driven: it should be as tall as its contents
// and say nothing about keys nobody is going to press.
func TestOnceIsPlainAndCompact(t *testing.T) {
	out := onceBoard(t, Options{})
	if strings.Contains(out, "\x1b[") {
		t.Error("printed board carries escape sequences")
	}
	for _, unwanted := range []string{"Q QUIT", "O OPEN", "? HELP", "ESC CLEAR"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("printed board offers %q", unwanted)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("line %d has trailing padding: %q", i, line)
		}
	}
	// Header, columns, rule, the demo's eight runs, a spacer and a footer.
	if got, want := len(lines), 3+8+2; got != want {
		t.Errorf("printed %d lines, want %d:\n%s", got, want, out)
	}
}

// Nothing blinks on paper: a BOARDING run caught mid-blink would simply be
// missing its status.
func TestOnceNeverBlinksAStatusAway(t *testing.T) {
	out := onceBoard(t, Options{})
	if n := strings.Count(out, "BOARDING"); n != 2 {
		t.Errorf("printed board shows BOARDING %d times, want the demo's 2:\n%s", n, out)
	}
}

func TestOnceHonoursViewAndSearch(t *testing.T) {
	failed := onceBoard(t, Options{View: ViewFailed})
	if !strings.Contains(failed, "SHOWING FAILED") || !strings.Contains(failed, "FAILED") {
		t.Errorf("--filter failed did not take:\n%s", failed)
	}
	if strings.Contains(failed, "DEPARTED") {
		t.Errorf("FAILED view printed a passing run:\n%s", failed)
	}

	search := onceBoard(t, Options{Search: "mobile"})
	if strings.Contains(search, "ACME/WEB") {
		t.Errorf("search printed a row it should have hidden:\n%s", search)
	}
	if !strings.Contains(search, "ACME/MOBILE") {
		t.Errorf("search hid the rows it should have kept:\n%s", search)
	}
}

func TestOnceRespectsMaxRows(t *testing.T) {
	out := onceBoard(t, Options{MaxRows: 3})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if got, want := len(lines), 3+3+2; got != want {
		t.Errorf("printed %d lines with MaxRows 3, want %d:\n%s", got, want, out)
	}
}
