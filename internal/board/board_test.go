package board

import (
	"testing"
	"time"

	"github.com/jzbz/clack/internal/ghapi"
)

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

func TestStatusWord(t *testing.T) {
	cases := []struct {
		status, concl, want string
	}{
		{"in_progress", "", "BOARDING"},
		{"queued", "", "QUEUED"},
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
		got := statusWord(ghapi.Run{Status: c.status, Conclusion: c.concl})
		if got != c.want {
			t.Errorf("statusWord(%q, %q) = %q, want %q", c.status, c.concl, got, c.want)
		}
	}
}

func TestShortDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
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
	if got := fit("ci", 5); got != "CI   " {
		t.Errorf("fit pad: got %q", got)
	}
	if got := fit("feature/long-branch-name", 8); got != "FEATURE/" {
		t.Errorf("fit truncate: got %q", got)
	}
	if got := fit("ab", 2); got != "AB" {
		t.Errorf("fit exact: got %q", got)
	}
}

func TestColumnsFitWidth(t *testing.T) {
	for _, w := range []int{70, 90, 110, 160} {
		total := 0
		for _, c := range columns(w) {
			total += c.width
		}
		if total > w {
			t.Errorf("width %d: columns total %d overflows", w, total)
		}
	}
}

func TestMergeKeepsRowIdentity(t *testing.T) {
	m := New(nil, time.Minute, 10)
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
}

func TestSelection(t *testing.T) {
	now := time.Now()
	m := New(nil, time.Minute, 10)
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
