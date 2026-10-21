package board

import (
	"context"
	"strings"
)

// onceHeight is tall enough that the panel is never the thing limiting a
// printed board; what it shows is capped by Options.MaxRows alone.
const onceHeight = 1 << 16

// Once polls once and returns a single settled board, for a cron job, a
// status line, or anything else that wants the picture without the TUI.
// Nothing animates: every flap starts on its target.
func Once(ctx context.Context, fetch Fetcher, opt Options, width int) (string, error) {
	m := New(fetch, opt)
	m.width, m.height = width, onceHeight
	m.printed = true
	res := fetch(ctx)
	m.merge(res.Runs)
	m.rl = res.RateLimit
	for _, r := range m.rows {
		r.fl.snap()
	}
	// Trailing blanks are padding for a live terminal; a printed board is
	// better off without them.
	var b strings.Builder
	for line := range strings.SplitSeq(m.View(), "\n") {
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteByte('\n')
	}
	return b.String(), res.Err
}
