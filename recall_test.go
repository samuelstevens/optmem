package main

import (
	"slices"
	"strings"
	"testing"
)

// Recall searches stretches of the log in parallel; merging what each found must equal one pass over the whole log, wherever the stretches are cut.
func FuzzMatchesMerge(f *testing.F) {
	f.Add([]byte("a bb ccc dddd eeeee"), 7, uint8(3))
	f.Add([]byte("x"), 1, uint8(1))
	f.Fuzz(func(t *testing.T, words []byte, limit int, cuts uint8) {
		if limit < 1 || limit > 1<<16 || cuts == 0 {
			t.Skip()
		}
		lines := strings.Fields(string(words))
		whole := matches{limit: limit}
		for _, l := range lines {
			whole.count++
			whole.keep(l)
		}
		merged := matches{limit: limit}
		w := int(cuts)
		for i := range w {
			part := matches{limit: limit}
			for _, l := range lines[len(lines)*i/w : len(lines)*(i+1)/w] {
				part.count++
				part.keep(l)
			}
			merged.merge(part)
		}
		if merged.count != whole.count || !slices.Equal(merged.newest, whole.newest) {
			t.Fatalf("merged %d %q, one pass %d %q", merged.count, merged.newest, whole.count, whole.newest)
		}
	})
}
