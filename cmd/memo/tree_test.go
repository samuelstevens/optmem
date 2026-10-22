package main

import "testing"

// cover tiles [0,T) with aligned power-of-two blocks, within budget, getting finer toward the present, and never asks for a block that cannot be built.
func FuzzCover(f *testing.F) {
	for _, T := range []int{1, 2, 3, 95, 96, 97, 300, 1000, 1023, 1024, 4096, 10000, 65536, 100003} {
		f.Add(T, 96)
	}
	f.Fuzz(func(t *testing.T, T, budget int) {
		// Two blocks per level always fit at alpha=1, so any budget of at least 64 is reachable for T < 2^31.
		if T < 1 || T > 1<<31-1 || budget < 64 || budget > 4096 {
			t.Skip()
		}
		c := cover(T, budget)
		if len(c) > budget {
			t.Fatalf("T=%d: %d lines > budget %d", T, len(c), budget)
		}
		if T <= budget && len(c) != T {
			t.Fatalf("T=%d fits budget %d but was compressed to %d lines", T, budget, len(c))
		}
		if c[0].lo != 0 || c[len(c)-1].hi != T {
			t.Fatalf("T=%d: cover spans [%d,%d)", T, c[0].lo, c[len(c)-1].hi)
		}
		for i, b := range c {
			if n := b.size(); n&(n-1) != 0 || b.lo%n != 0 {
				t.Fatalf("T=%d: %v is not an aligned power-of-two block", T, b)
			}
			if b.hi > T {
				t.Fatalf("T=%d: %v reaches into the future", T, b)
			}
			if i > 0 && (c[i-1].hi != b.lo || b.size() > c[i-1].size()) {
				t.Fatalf("T=%d: %v after %v is a gap, overlap, or coarser", T, b, c[i-1])
			}
		}
	})
}
