package main

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// A block is an aligned power-of-two range of memories, [lo,hi), compressed into one line. Blocks form a binary merge tree over LOG.txt: block [lo,hi) is the compression of [lo,mid) and [mid,hi).
type block struct{ lo, hi int }

func (b block) size() int { return b.hi - b.lo }

func (b block) mid() int { return (b.lo + b.hi) / 2 }

func (b block) halves() [2]block { return [2]block{{b.lo, b.mid()}, {b.mid(), b.hi}} }

// String is the id wake and the nap prompts print: inclusive at both ends.
func (b block) String() string { return fmt.Sprintf("%d-%d", b.lo, b.hi-1) }

var blockRe = regexp.MustCompile(`^(\d+)-(\d+)$`)

// parseBlock reads `<lo>-<hi>` as wake prints it, and insists on a real block -- an aligned power-of-two range. Without the shape check, `4-5` and `5-6` read the same record.
func parseBlock(s string) (block, error) {
	m := blockRe.FindStringSubmatch(s)
	if m == nil {
		return block{}, fmt.Errorf("'%s' is not a block id; copy it from the prompt", s)
	}
	lo, err1 := strconv.Atoi(m[1])
	hi, err2 := strconv.Atoi(m[2])
	b := block{lo, hi + 1}
	n := b.size()
	if err1 != nil || err2 != nil || n < 2 || n&(n-1) != 0 || lo%n != 0 {
		return block{}, fmt.Errorf("%s is not a block; copy an id printed by wake, like 16-31", s)
	}
	return b, nil
}

// coverAt tiles [0,T) with aligned power-of-two blocks, keeping a block whole iff its size is at most alpha times its age. Bigger alpha = coarser = fewer lines. The walk is depth-first, left half first, so the tiles come out in order.
func coverAt(T int, alpha float64) []block {
	root := 1
	for root < T {
		root *= 2
	}
	var out []block
	stack := []block{{0, root}}
	for len(stack) > 0 {
		b := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if b.lo >= T {
			continue
		}
		if b.size() > 1 && (b.hi > T || float64(b.size()) > alpha*float64(T-b.lo)) {
			h := b.halves()
			stack = append(stack, h[1], h[0])
		} else {
			out = append(out, b)
		}
	}
	return out
}

// cover is the blocks `memo wake` prints: at most `budget` of them, finest near T. Detail decays with age, so recent memories stay verbatim and ancient ones collapse. If everything fits, nothing is compressed at all.
func cover(T, budget int) []block {
	if T <= 0 {
		return nil
	}
	if T <= budget {
		out := make([]block, T)
		for i := range out {
			out[i] = block{i, i + 1}
		}
		return out
	}
	lo, hi := 0.0, 1.0
	for range 60 {
		mid := (lo + hi) / 2
		if len(coverAt(T, mid)) > budget {
			lo = mid
		} else {
			hi = mid
		}
	}
	out := coverAt(T, hi)
	// Block sizes jump in powers of two, so alpha alone can undershoot the budget. Spend what is left on the present, where detail is worth most.
	for len(out) < budget {
		i := len(out) - 1
		for i >= 0 && out[i].size() == 1 {
			i--
		}
		if i < 0 {
			break
		}
		h := out[i].halves()
		out = slices.Replace(out, i, i+1, h[0], h[1])
	}
	return out
}
