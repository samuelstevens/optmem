package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// template is the block `memo init` prints for the user to paste into AGENTS.md.
//
//go:embed template.md
var template string

// rawMax is the largest block a nap compresses straight from the raw log; bigger blocks compress from their two halves' summaries.
const rawMax = 16

// nextNap is the prompt for the next compression, or "" if none is due.
func (m *memo) nextNap(T int) (string, error) {
	b, ok, err := m.nextPending(T)
	if err != nil || !ok {
		return "", err
	}
	n, err := m.pendingCount(T)
	if err != nil {
		return "", err
	}
	left := n - 1
	var body []string
	if b.size() <= rawMax {
		es, err := m.logSlice(b.lo, b.hi)
		if err != nil {
			return "", err
		}
		for _, e := range es {
			body = append(body, "  "+e.String())
		}
	} else {
		for _, h := range b.halves() {
			s, ok, err := m.treeGet(h)
			if err != nil {
				return "", err
			}
			if !ok {
				// A block is pending only after its halves settled, so a missing half is a blank record -- a corrupt write. Drop it and the next nap rebuilds it.
				return "", fmt.Errorf("the summary of #%s is blank; run: %s forget %s", h, m.me, h)
			}
			body = append(body, fmt.Sprintf("  #%s %s", h, s))
		}
	}
	tail := ""
	switch {
	case left == 1:
		tail = "\n1 compression remains after this one."
	case left > 1:
		tail = fmt.Sprintf("\n%d compressions remain after this one.", left)
	}
	return fmt.Sprintf("Compress memories #%s into one line of at most %d bytes.\n"+
		"Keep what has lasting effect, drop what does not. Invent nothing.\n\n"+
		"%s\n%s\n"+
		"Run: %s nap %s \"<your line>\"",
		b, m.sz["ENTRY_CHARS"], strings.Join(body, "\n"), tail, m.me, b), nil
}

// check validates one memory or summary: one line, valid UTF-8, within the byte limit.
func (m *memo) check(text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", errors.New("empty: a memory is one line of text")
	case strings.ContainsAny(text, "\n\r"):
		return "", fmt.Errorf("%d lines: a memory is one line; merge them, or note them separately", strings.Count(text, "\n")+1)
	case !utf8.ValidString(text):
		return "", errors.New("not UTF-8: a memory is UTF-8 text")
	case len(text) > m.sz["ENTRY_CHARS"]:
		return "", fmt.Errorf("too long: %d bytes, limit %d; accented characters cost 2 bytes; compress it further", len(text), m.sz["ENTRY_CHARS"])
	}
	return text, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if w, ok := strings.CutSuffix(word, "y"); ok {
		word = w + "ie"
	} else if strings.HasSuffix(word, "s") || strings.HasSuffix(word, "h") || strings.HasSuffix(word, "x") {
		word += "e"
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// cmdInit is the one command that may create the memory directory, and the whole setup: make the store, write the size knobs, print the block the user pastes into their agent's instruction file. Re-running it is safe: it only ever creates what is missing, and never rewrites what is there.
func cmdInit(m *memo, args []string) error {
	fi, err := os.Stat(m.dir)
	fresh := err != nil || !fi.IsDir()
	if err := os.MkdirAll(filepath.Join(m.dir, "TREE"), 0o777); err != nil {
		return err
	}
	if err := touch(m.logPath()); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.dir, "config")); errors.Is(err, fs.ErrNotExist) {
		if err := writeConfig(m.dir, m.me, nil); err != nil {
			return err
		}
	}
	if err := m.open(); err != nil {
		return err
	}
	if fresh {
		m.printf("Created %s: this machine's memory, one identity, forever.\n", pretty(m.dir))
	} else {
		T, err := m.logLen()
		if err != nil {
			return err
		}
		m.printf("Found %s: %s.\n", pretty(m.dir), plural(T, "memory"))
	}
	m.printf("Sizes live in %s/config; the defaults are fine.\n\n", pretty(m.dir))
	m.printf("Paste this at the top of your agent's AGENTS.md (or CLAUDE.md), done:\n\n")
	r := strings.NewReplacer("{memo}", m.me, "{data}", pretty(m.dir), "{chars}", strconv.Itoa(m.sz["ENTRY_CHARS"]))
	m.printf("%s\n", r.Replace(strings.TrimSpace(template)))
	return nil
}

func cmdWake(m *memo, args []string) error {
	now, err := m.logLen()
	if err != nil {
		return err
	}
	k, T := 1, now
	if len(args) > 0 {
		var nums []int
		for _, a := range args {
			n, err := strconv.Atoi(a)
			if err == nil && strings.TrimLeft(a, "0123456789") == "" {
				nums = append(nums, n)
			}
		}
		if len(nums) != len(args) {
			return fmt.Errorf("usage: %s wake [part [T]]", m.me)
		}
		k = nums[0]
		if len(nums) == 2 {
			T = nums[1]
			if T > now {
				return fmt.Errorf("snapshot %d is past the log, which holds %s; run: %s wake", T, plural(now, "memory"), m.me)
			}
		}
	}
	// A part is rendered as of T, so a note landing between two parts cannot shift a boundary and drop a line.
	if T == 0 {
		m.printf("No memories yet. Record the first with: %s note \"<one line>\"\n", m.me)
		m.printf("You are awake.\n")
		return nil
	}
	var lines []string
	for _, b := range cover(T, m.sz["WAKE_LINES"]) {
		if b.size() == 1 {
			e, err := m.logGet(b.lo)
			if err != nil {
				return err
			}
			lines = append(lines, e.String())
			continue
		}
		s, ok, err := m.treeGet(b)
		if err != nil {
			return err
		}
		if !ok {
			nap, err := m.nextNap(T)
			if err != nil {
				return err
			}
			if nap != "" {
				// The ONLY reason to refuse: this document cannot be written without that summary. Work that the document does not need is handed over after the read instead, costing no round trip.
				n, err := m.pendingCount(T)
				if err != nil {
					return err
				}
				m.printf("Cannot wake: the memory context needs #%s, which is not compressed yet.\n"+
					"Do the %s below, then run %s wake again.\n\n%s\n",
					b, plural(n, "compression"), m.me, nap)
				return errRefused
			}
			// A parallel session may have paid it meanwhile.
			if s, ok, err = m.treeGet(b); err != nil {
				return err
			}
		}
		if !ok {
			// Nothing is pending, so the record exists but is blank -- a corrupt write. Drop it and the next nap rebuilds it.
			return fmt.Errorf("the summary of #%s is blank; run: %s forget %s", b, m.me, b)
		}
		lines = append(lines, fmt.Sprintf("#%s %s", b, s))
	}
	// The document goes out in parts that survive any harness's output cap.
	var parts [][]string
	var cur []string
	size := 0
	for _, line := range lines {
		n := len(line) + 1
		if len(cur) > 0 && (len(cur) >= m.sz["PART_LINES"] || size+n > m.sz["PART_CHARS"]) {
			parts = append(parts, cur)
			cur, size = nil, 0
		}
		cur = append(cur, line)
		size += n
	}
	if len(cur) > 0 {
		parts = append(parts, cur)
	}
	if k < 1 || k > len(parts) {
		return fmt.Errorf("no part %d: the memory has %s; run: %s wake", k, plural(len(parts), "part"), m.me)
	}
	if len(parts) > 1 {
		// The count is here so the T in `memo wake 2 296` reads as what it is: the snapshot this document was written from.
		m.printf("Your memory, part %d of %d, oldest first (%s).\n", k, len(parts), plural(T, "memory"))
	}
	m.printf("%s\n", strings.Join(parts[k-1], "\n"))
	if k < len(parts) {
		// This footer is the only instruction that survives every harness's truncation (pi drops the HEAD of a long output), so it has to say both that the read is unfinished and how to continue it.
		m.printf("Not awake yet. Run: %s wake %d %d\n", m.me, k+1, T)
		return nil
	}
	// Always, even for a one-part memory: the contract an agent is given is "run parts until one says awake", so it must always arrive.
	m.printf("You are awake.\n")
	nap, err := m.nextNap(T)
	if err != nil {
		return err
	}
	if nap != "" {
		m.printf("\n%s\n", nap)
	}
	return nil
}

func cmdNote(m *memo, args []string) error {
	text, err := m.check(args[0])
	if err != nil {
		return err
	}
	i, err := m.logAppend([]entry{{date: time.Now().Format(time.DateOnly), text: text}})
	if err != nil {
		return err
	}
	m.printf("Saved as #%d.\n", i)
	nap, err := m.nextNap(i + 1)
	if err != nil {
		return err
	}
	if nap != "" {
		m.printf("\n%s\n", nap)
	}
	return nil
}

func cmdNap(m *memo, args []string) error {
	T, err := m.logLen()
	if err != nil {
		return err
	}
	said := len(args) > 0
	if said {
		if len(args) != 2 {
			return fmt.Errorf("usage: %s nap <lo>-<hi> \"<one line>\"", m.me)
		}
		b, err := parseBlock(args[0])
		if err != nil {
			return err
		}
		next, ok, err := m.nextPending(T)
		if err != nil {
			return err
		}
		if !ok {
			m.printf("Nothing left to compress.\n")
			return nil
		}
		if b != next {
			_, settled, err := m.treeGet(b)
			if err != nil {
				return err
			}
			if !settled {
				return fmt.Errorf("wrong block %s: blocks are built in order, and the next is %s; run: %s nap", args[0], next, m.me)
			}
			m.printf("%s is already settled.\n", b)
		} else {
			text, err := m.check(args[1])
			if err != nil {
				return err
			}
			// Blocks are built in order, so a summary only ever appends one record to one level file. The lock is held to the end of the command; everything after the write only reads.
			l, err := m.lock()
			if err != nil {
				return err
			}
			defer l.Close()
			p := m.treePath(b.size())
			if err := repair(p, treeRec); err != nil {
				return err
			}
			n, err := count(p, treeRec)
			if err != nil {
				return err
			}
			if n != b.lo/b.size() {
				m.printf("%s was settled or forgotten meanwhile.\n", b)
			} else {
				rec, err := pad(text, treeRec)
				if err != nil {
					return err
				}
				if err := appendSynced(p, rec); err != nil {
					return err
				}
				m.printf("%s saved.\n", b)
			}
		}
	}
	nap, err := m.nextNap(T)
	if err != nil {
		return err
	}
	if nap == "" {
		m.printf("Nothing left to compress.\n")
		return nil
	}
	if said {
		m.printf("\n")
	}
	m.printf("%s\n", nap)
	return nil
}

// cmdZoom opens one node of the tree: its two halves, each rendered as wake renders it -- a summary, or the raw memory once a half is single. The navigating intelligence is the agent's; the tool only reads.
func cmdZoom(m *memo, args []string) error {
	b, err := parseBlock(args[0])
	if err != nil {
		return err
	}
	T, err := m.logLen()
	if err != nil {
		return err
	}
	if b.lo >= T {
		return fmt.Errorf("#%s is beyond the memory, which holds %s; run: %s wake", args[0], plural(T, "memory"), m.me)
	}
	for _, h := range b.halves() {
		if h.lo >= T {
			continue // the future: no memories there yet
		}
		if h.size() == 1 {
			e, err := m.logGet(h.lo)
			if err != nil {
				return err
			}
			m.printf("%s\n", e)
			continue
		}
		s, ok, err := m.treeGet(h)
		if err != nil {
			return err
		}
		if !ok {
			s = "not compressed yet"
		}
		m.printf("#%s %s\n", h, s)
	}
	return nil
}

// cmdForget drops a summary that is wrong -- mistyped, or a bad compression -- and everything built on top of it, by truncating each level back to that point; the next nap computes them again. Later blocks at those levels go too and are rebuilt. The log is untouched, so nothing is ever actually lost.
func cmdForget(m *memo, args []string) error {
	b, err := parseBlock(args[0])
	if err != nil {
		return err
	}
	l, err := m.lock()
	if err != nil {
		return err
	}
	defer l.Close()
	T, err := m.logLen()
	if err != nil {
		return err
	}
	var gone []block
	for size := b.size(); size <= T; size *= 2 {
		p, k := m.treePath(size), b.lo/size
		n, err := count(p, treeRec)
		if err != nil {
			return err
		}
		if n <= k {
			continue
		}
		for i := k; i < n; i++ {
			gone = append(gone, block{i * size, (i + 1) * size})
		}
		if err := os.Truncate(p, int64(k)*treeRec); err != nil {
			return err
		}
	}
	if len(gone) == 0 {
		return fmt.Errorf("no summary at %s", args[0])
	}
	m.printf("Forgot %s, from %s up. Run: %s nap\n", plural(len(gone), "summary"), gone[0], m.me)
	return nil
}

// matches is what a search found: how many lines, and the longest run of the newest that fits in limit bytes -- a vague regex matches the whole log, and the whole log does not fit in a harness's output or in memory.
type matches struct {
	count, size, limit int
	newest             []string
}

func (s *matches) keep(line string) {
	s.newest = append(s.newest, line)
	s.size += len(line) + 1
	for s.size > s.limit {
		s.size -= len(s.newest[0]) + 1
		s.newest = s.newest[1:]
	}
}

// merge adds what a later stretch of the log found. If that stretch dropped a line, its run is already as long as any run reaching back here could be, so it replaces ours; otherwise its lines just extend ours.
func (s *matches) merge(later matches) {
	s.count += later.count
	if len(later.newest) < later.count {
		s.newest, s.size = later.newest, later.size
		return
	}
	for _, line := range later.newest {
		s.keep(line)
	}
}

func cmdRecall(m *memo, args []string) error {
	// Compiled bare first, so an error quotes what the user typed.
	if _, err := regexp.Compile(args[0]); err != nil {
		return fmt.Errorf("bad regex: %w", err)
	}
	pat := regexp.MustCompile("(?i)" + args[0])
	T, err := m.logLen()
	if err != nil {
		return err
	}
	// The log is split into one contiguous stretch per core, searched in parallel, and the stretches merged oldest first.
	w := runtime.GOMAXPROCS(0)
	found := make([]matches, w)
	errs := make([]error, w)
	var wg sync.WaitGroup
	for i := range w {
		found[i].limit = m.sz["PART_CHARS"]
		wg.Go(func() {
			// Records are streamed, never held: at a million memories the log is 300 MB.
			f, err := os.Open(m.logPath())
			if err != nil {
				errs[i] = err
				return
			}
			defer f.Close()
			lo, hi := T*i/w, T*(i+1)/w
			r := io.NewSectionReader(f, int64(lo)*logRec, int64(hi-lo)*logRec)
			buf := make([]byte, logRec*4096)
			for base := lo; ; {
				n, err := io.ReadFull(r, buf)
				for k := range n / logRec {
					e, err := m.decode(base+k, buf[k*logRec:(k+1)*logRec])
					if err != nil {
						errs[i] = err
						return
					}
					if line := e.String(); pat.MatchString(line) {
						found[i].count++
						found[i].keep(line)
					}
				}
				base += n / logRec
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return
				}
				if err != nil {
					errs[i] = err
					return
				}
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	all := matches{limit: m.sz["PART_CHARS"]}
	for _, f := range found {
		all.merge(f)
	}
	if all.count == 0 {
		m.printf("No match.\n")
		return nil
	}
	m.printf("%s\n", strings.Join(all.newest, "\n"))
	if len(all.newest) < all.count {
		m.printf("Newest %d of %s. Narrow the regex.\n", len(all.newest), plural(all.count, "match"))
	} else {
		m.printf("%s.\n", plural(all.count, "match"))
	}
	return nil
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// cmdImport bulk-appends historical memories, 'YYYY-MM-DD <text>' per line, to bootstrap an identity from older records. Used once.
func cmdImport(m *memo, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("%s is not UTF-8 text; convert it, then import again", pretty(args[0]))
	}
	T, err := m.logLen()
	if err != nil {
		return err
	}
	last := "0000-00-00"
	if T > 0 {
		e, err := m.logGet(T - 1)
		if err != nil {
			return err
		}
		last = e.date
	}
	var out []entry
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		date, text, _ := strings.Cut(line, " ")
		if !dateRe.MatchString(date) {
			return fmt.Errorf("line %d: expected 'YYYY-MM-DD <text>', got: %s", i+1, line)
		}
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return fmt.Errorf("line %d: %s is not a real date", i+1, date)
		}
		if date < last {
			return fmt.Errorf("line %d: date %s precedes the previous memory (%s)", i+1, date, last)
		}
		text = strings.TrimSpace(text)
		if text == "" || len(text) > m.sz["ENTRY_CHARS"] {
			return fmt.Errorf("line %d: %d bytes, limit %d", i+1, len(text), m.sz["ENTRY_CHARS"])
		}
		out = append(out, entry{date: date, text: text})
		last = date
	}
	if len(out) == 0 {
		return fmt.Errorf("%s has no memories", args[0])
	}
	base, err := m.logAppend(out)
	if err != nil {
		return err
	}
	m.printf("Imported %s, #%d to #%d.\n", plural(len(out), "memory"), base, base+len(out)-1)
	n, err := m.pendingCount(base + len(out))
	if err != nil {
		return err
	}
	if n > 0 {
		m.printf("%s pending. Run: %s nap\n", plural(n, "compression"), m.me)
	}
	return nil
}
