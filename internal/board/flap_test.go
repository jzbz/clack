package board

import (
	"strings"
	"testing"
)

// converge steps f until it settles, failing if that takes unreasonably long
// or any intermediate glyph leaves the ramp.
func converge(t *testing.T, f *flap) {
	t.Helper()
	for i := 0; i < 5000 && !f.settled(); i++ {
		f.step()
		for _, r := range f.String() {
			if !strings.ContainsRune(glyphRamp, r) {
				t.Fatalf("step %d: glyph %q left the ramp", i, r)
			}
		}
	}
	if !f.settled() {
		t.Fatalf("did not settle, stuck at %q", f.String())
	}
}

func TestFlapConverges(t *testing.T) {
	var f flap
	f.setTarget("BOARDING  12M")
	converge(t, &f)
	if got := f.String(); got != "BOARDING  12M" {
		t.Fatalf("settled on %q, want %q", got, "BOARDING  12M")
	}
}

func TestFlapRetarget(t *testing.T) {
	var f flap
	f.setTarget("QUEUED")
	converge(t, &f)
	f.setTarget("DEPARTED")
	converge(t, &f)
	if got := f.String(); got != "DEPARTED" {
		t.Fatalf("settled on %q, want %q", got, "DEPARTED")
	}
}

func TestFlapNoopWhenOnTarget(t *testing.T) {
	var f flap
	f.setTarget("CI")
	converge(t, &f)
	before := f.String()
	f.setTarget("CI")
	f.step()
	if got := f.String(); got != before {
		t.Fatalf("on-target setTarget flapped: %q -> %q", before, got)
	}
}

func TestNextGlyphWraps(t *testing.T) {
	last := rampRunes[len(rampRunes)-1]
	if got := nextGlyph(last, 'A'); got != ' ' {
		t.Fatalf("nextGlyph(%q, 'A') = %q, want ' '", last, got)
	}
	if got := nextGlyph(' ', 'A'); got != 'A' {
		t.Fatalf("nextGlyph(' ', 'A') = %q, want 'A'", got)
	}
	// A rune the board has no flap for snaps straight to the target rather
	// than spinning forever looking for a position it will never find.
	const offRamp = '\u00a7'
	if rampPos(offRamp) >= 0 {
		t.Fatalf("%q is on the ramp; pick another rune for this test", offRamp)
	}
	if got := nextGlyph(offRamp, 'B'); got != 'B' {
		t.Fatalf("nextGlyph(%q, 'B') = %q, want 'B'", offRamp, got)
	}
}

func TestFlapStagger(t *testing.T) {
	var f flap
	f.setTarget("AB")
	f.age = 0
	f.step() // age becomes 1; position 1 still staggered (1 <= 1/6 is false, so it moves)
	// Position 0 always animates immediately from blank.
	var g flap
	g.setTarget(strings.Repeat("Z", 60))
	g.age = 0
	g.step()
	s := g.String()
	if s[0] == ' ' {
		t.Fatal("first cell did not flip on the first tick")
	}
	// Cell 59 needs age > 59/6 = 9 ticks; after one it must still be blank.
	if s[59] != ' ' {
		t.Fatalf("stagger broken: cell 59 already shows %q after one tick", s[59])
	}
}
