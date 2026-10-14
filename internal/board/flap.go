package board

import "strings"

// glyphRamp is the ordered character set of one split-flap cell. A changing
// cell steps forward through the ramp until it lands on its target.
const glyphRamp = " ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-/:'_•"

var rampRunes = []rune(glyphRamp)

// flapStagger delays a cell's first flip by (column / flapStagger) ticks, so
// updates sweep across the row left to right instead of flipping at once.
const flapStagger = 6

// flap is one row of split-flap text: what is shown (cur) flips toward what
// should be shown (dst).
type flap struct {
	cur []rune
	dst []rune
	age int // ticks since dst last changed
}

// setTarget aims the row at s. Nothing happens if s is already the target.
func (f *flap) setTarget(s string) {
	if string(f.dst) == s {
		return
	}
	f.dst = []rune(s)
	if len(f.cur) != len(f.dst) {
		cur := make([]rune, len(f.dst))
		copy(cur, f.cur)
		for i, r := range cur {
			if r == 0 {
				cur[i] = ' '
			}
		}
		f.cur = cur
	}
	f.age = 0
}

// step advances every unsettled cell one glyph, honouring the stagger.
func (f *flap) step() {
	f.age++
	for i, r := range f.cur {
		if r == f.dst[i] || f.age <= i/flapStagger {
			continue
		}
		f.cur[i] = nextGlyph(r, f.dst[i])
	}
}

// settled reports whether every cell shows its target.
func (f *flap) settled() bool {
	for i, r := range f.cur {
		if r != f.dst[i] {
			return false
		}
	}
	return true
}

func (f *flap) String() string { return string(f.cur) }

// nextGlyph returns the glyph one step past cur on the way to dst.
func nextGlyph(cur, dst rune) rune {
	ci := strings.IndexRune(glyphRamp, cur)
	di := strings.IndexRune(glyphRamp, dst)
	if ci < 0 || di < 0 {
		return dst
	}
	return rampRunes[(ci+1)%len(rampRunes)]
}
