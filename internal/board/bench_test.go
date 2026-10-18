package board

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// benchBoard is a settled, fully populated board at a typical terminal size.
func benchBoard(tb testing.TB) Model {
	tb.Helper()
	fetch := Demo()
	m := New(fetch, time.Minute, 12)
	m = step(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = step(m, runsMsg(fetch(tb.Context())))
	return frames(m, 400)
}

func BenchmarkView(b *testing.B) {
	m := benchBoard(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}

func BenchmarkFrame(b *testing.B) {
	m := benchBoard(b)
	b.ReportAllocs()
	for b.Loop() {
		m = frame(m)
		_ = m.View()
	}
}

func BenchmarkMerge(b *testing.B) {
	fetch := Demo()
	m := New(fetch, time.Minute, 12)
	m = step(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	res := fetch(b.Context())
	b.ReportAllocs()
	for b.Loop() {
		m.merge(res.Runs)
	}
}

func BenchmarkFlapConverge(b *testing.B) {
	const target = "20:13 ACME/WEB       CI           MAIN         OCTOCAT     BOARDING   4M"
	b.ReportAllocs()
	for b.Loop() {
		var f flap
		f.setTarget(target)
		for !f.settled() {
			f.step()
		}
	}
}
