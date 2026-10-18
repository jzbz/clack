package board

import "unicode/utf8"

// glyphRamp is the ordered character set of one split-flap cell: everything
// the board's physical flaps can show. A changing cell steps forward through
// the ramp until it lands on its target.
const glyphRamp = " ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.,:-_/'()[]&+#@!?*•"

// unknownGlyph stands in for a character the board has no flap for. It is
// itself on the ramp, and one cell wide, so columns stay aligned whatever
// turns up in a branch or workflow name.
const unknownGlyph = '•'

// flapStagger delays a cell's first flip by (column / flapStagger) ticks, so
// updates sweep across the row left to right instead of flipping at once.
const flapStagger = 6

var (
	rampRunes = []rune(glyphRamp)

	// asciiRamp maps an ASCII glyph to its ramp position, -1 when the board
	// cannot show it. A flat table keeps nextGlyph free of string scans.
	asciiRamp [128]int8

	// unknownPos is unknownGlyph's position on the ramp.
	unknownPos int
)

func init() {
	for i := range asciiRamp {
		asciiRamp[i] = -1
	}
	for i, r := range rampRunes {
		switch {
		case r < 128:
			asciiRamp[r] = int8(i)
		case r == unknownGlyph:
			unknownPos = i
		}
	}
}

// rampPos is r's position on the ramp, or -1 if the board has no such flap.
func rampPos(r rune) int {
	if r < 128 {
		return int(asciiRamp[r])
	}
	if r == unknownGlyph {
		return unknownPos
	}
	return -1
}

// glyph maps r onto the nearest thing the board can print.
func glyph(r rune) rune {
	if rampPos(r) < 0 {
		return unknownGlyph
	}
	return r
}

// flap is one row of split-flap text: what is shown (cur) flips toward what
// should be shown (dst), one glyph per tick.
type flap struct {
	cur    []rune
	dst    []rune
	age    int // ticks since dst last changed
	moving int // cells not yet showing their target
}

// setTarget aims the row at s. Nothing happens if s is already the target,
// so a row whose text is recomputed but unchanged keeps flapping undisturbed.
func (f *flap) setTarget(s string) {
	if sameRunes(f.dst, s) {
		return
	}
	if n := utf8.RuneCountInString(s); cap(f.dst) < n {
		f.dst = make([]rune, 0, n)
	}
	f.dst = f.dst[:0]
	for _, r := range s {
		f.dst = append(f.dst, r)
	}
	// Cells added by a longer target start blank and flap up from there.
	for len(f.cur) < len(f.dst) {
		f.cur = append(f.cur, ' ')
	}
	f.cur = f.cur[:len(f.dst)]

	f.moving = 0
	for i, r := range f.cur {
		if r != f.dst[i] {
			f.moving++
		}
	}
	f.age = 0
}

// step advances every unsettled cell one glyph, honouring the stagger.
func (f *flap) step() {
	if f.moving == 0 {
		return
	}
	f.age++
	for i, r := range f.cur {
		if r == f.dst[i] || f.age <= i/flapStagger {
			continue
		}
		next := nextGlyph(r, f.dst[i])
		f.cur[i] = next
		if next == f.dst[i] {
			f.moving--
		}
	}
}

// settled reports whether every cell shows its target.
func (f *flap) settled() bool { return f.moving == 0 }

func (f *flap) String() string { return string(f.cur) }

// nextGlyph returns the glyph one step past cur on the way to dst. A target
// the ramp cannot reach snaps into place rather than spinning forever.
func nextGlyph(cur, dst rune) rune {
	ci, di := rampPos(cur), rampPos(dst)
	if ci < 0 || di < 0 {
		return dst
	}
	return rampRunes[(ci+1)%len(rampRunes)]
}

// sameRunes reports whether r holds exactly the runes of s, without the
// allocation a []rune(s) conversion or string(r) would cost.
func sameRunes(r []rune, s string) bool {
	i := 0
	for _, c := range s {
		if i == len(r) || r[i] != c {
			return false
		}
		i++
	}
	return i == len(r)
}
