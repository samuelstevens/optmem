package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Verified caps of the harnesses in the wild: Claude Code cuts a command's output at 30,000 chars (middle), pi at 50 KB / 2000 lines (head), Codex budgets 10,000 tokens. A part must fit the strictest of each kind.
const capChars, capLines = 30000, 2000

type result struct {
	code     int
	out, err string
}

// memoIn runs one command in-process against the memory at dir.
func memoIn(t *testing.T, dir string, args ...string) result {
	t.Helper()
	t.Setenv("MEMORY_DIR", dir)
	var out, errb bytes.Buffer
	code := run("memo", args, &out, &errb)
	return result{code, out.String(), errb.String()}
}

// newMemory is an initialized, empty memory.
func newMemory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "memory")
	if r := memoIn(t, dir, "init"); r.code != 0 {
		t.Fatalf("init failed: %s", r.err)
	}
	return dir
}

var napRe = regexp.MustCompile(`memo nap (\d+-\d+) `)

// settle answers every pending compression with summarize(prompt) and returns how many it answered.
func settle(t *testing.T, dir string, summarize func(prompt string) string) int {
	t.Helper()
	n := 0
	for {
		r := memoIn(t, dir, "nap")
		if r.code != 0 {
			t.Fatalf("nap failed: %s", r.err)
		}
		m := napRe.FindStringSubmatch(r.out)
		if m == nil {
			if !strings.Contains(r.out, "Nothing left to compress") {
				t.Fatalf("nap neither prompted nor finished:\n%s", r.out)
			}
			return n
		}
		if r := memoIn(t, dir, "nap", m[1], summarize(r.out)); r.code != 0 {
			t.Fatalf("nap %s rejected: %s", m[1], r.err)
		}
		n++
	}
}

// joinBody is a fake compressor: the prompt's memories, joined and cut.
func joinBody(prompt string) string {
	var body []string
	for l := range strings.SplitSeq(prompt, "\n") {
		if strings.HasPrefix(l, "  #") {
			body = append(body, strings.TrimSpace(l))
		}
	}
	s := strings.Join(body, " ")
	for len(s) > 280 {
		s = s[:len(s)-1]
	}
	return strings.ToValidUTF8(strings.TrimSpace(s), "")
}

func constant(s string) func(string) string { return func(string) string { return s } }

// buildable is how many blocks T memories make: every aligned power-of-two block of size >= 2 that lies wholly in the past.
func buildable(T int) int {
	n := 0
	for size := 2; size <= T; size *= 2 {
		n += T / size
	}
	return n
}

// importDays writes n dated memories and imports them.
func importDays(t *testing.T, dir string, n int, text func(i int) string) {
	t.Helper()
	var b strings.Builder
	day := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		fmt.Fprintf(&b, "%s %s\n", day.AddDate(0, 0, i/5).Format(time.DateOnly), text(i))
	}
	p := filepath.Join(t.TempDir(), "seed.txt")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := memoIn(t, dir, "import", p); r.code != 0 {
		t.Fatalf("import failed: %s", r.err)
	}
}

func longMemory(i int) string {
	return fmt.Sprintf("memory number %d, a thing that happened, was weighed against the rest "+
		"of the week, turned out to matter more than anyone guessed at the time, and left a "+
		"mark on every plan that followed it", i)
}

// fingerprint is every file of a memory but the lock.
func fingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == ".lock" {
			return err
		}
		data, err := os.ReadFile(p)
		out[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// scribble writes data into a file the way a crash or a hand edit would: over its start, or appended with os.O_APPEND.
func scribble(t *testing.T, path string, flag int, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|flag, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

// buildMemo compiles the real binary, for what only real processes can show.
func buildMemo(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "memo")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// ---------------------------------------------------------------- store

// A typo in MEMORY_DIR must not silently open a second, empty identity; only init creates a memory, and re-running it never changes one.
func TestOnlyInitCreatesMemory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	r := memoIn(t, dir, "wake")
	if r.code != 1 || !strings.Contains(r.err, "no memory at") || !strings.Contains(r.err, "memo init") {
		t.Fatalf("wake on a missing memory: %+v", r)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("wake created the missing memory")
	}

	r = memoIn(t, dir, "init")
	if r.code != 0 || !strings.Contains(r.out, "## Memory") || !strings.Contains(r.out, "You are a subagent") {
		t.Fatalf("init did not print the setup block: %+v", r)
	}
	if r := memoIn(t, dir, "wake"); r.code != 0 || !strings.HasSuffix(r.out, "You are awake.\n") {
		t.Fatalf("wake after init: %+v", r)
	}

	memoIn(t, dir, "note", "something")
	memoIn(t, dir, "config", "WAKE_LINES=12")
	before := fingerprint(t, dir)
	for range 3 {
		if r := memoIn(t, dir, "init"); r.code != 0 || !strings.Contains(r.out, "Found") {
			t.Fatalf("init on a live memory: %+v", r)
		}
	}
	if after := fingerprint(t, dir); !maps.Equal(after, before) {
		t.Fatal("init modified an existing memory")
	}
}

// Without $MEMORY_DIR the memory lives in the XDG data directory; a relative $XDG_DATA_HOME is invalid by the spec and ignored.
func TestMemoryLocation(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	for _, tc := range []struct{ memoryDir, xdgDataHome, want string }{
		{"", "", filepath.Join(home, ".local", "share", "optmem")},
		{"", data, filepath.Join(data, "optmem")},
		{"", "relative/data", filepath.Join(home, ".local", "share", "optmem")},
		{filepath.Join(data, "elsewhere"), data, filepath.Join(data, "elsewhere")},
	} {
		t.Setenv("HOME", home)
		t.Setenv("MEMORY_DIR", tc.memoryDir)
		t.Setenv("XDG_DATA_HOME", tc.xdgDataHome)
		var out, errb bytes.Buffer
		if code := run("memo", []string{"init"}, &out, &errb); code != 0 {
			t.Fatalf("%+v: init failed: %s", tc, errb.String())
		}
		if _, err := os.Stat(filepath.Join(tc.want, "LOG.txt")); err != nil {
			t.Errorf("%+v: no memory at %s: %v", tc, tc.want, err)
		}
		if err := os.RemoveAll(tc.want); err != nil {
			t.Fatal(err)
		}
	}
}

// A store opened by any command but init never writes its own config: the defaults would be frozen in it, and a tool update could not change them.
func TestDefaultsAreNotFrozen(t *testing.T) {
	dir := t.TempDir()
	memoIn(t, dir, "note", "a memory")
	if _, err := os.Stat(filepath.Join(dir, "config")); err == nil {
		t.Fatal("a store wrote its own config")
	}
}

// The filesystem is the one thing the tool does not control: its failures are reported in the tool's voice, naming what went wrong.
func TestFilesystemErrorsAreReported(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := memoIn(t, file, "init")
	if r.code != 1 || !strings.Contains(r.err, "not a directory") {
		t.Fatalf("init into a file: %+v", r)
	}
}

// ---------------------------------------------------------------- notes

func TestNoteIsOneBoundedLine(t *testing.T) {
	dir := newMemory(t)
	for _, tc := range []struct{ text, want string }{
		{strings.Repeat("x", 281), "too long"},
		{strings.Repeat("ã", 150), "300 bytes"}, // multi-byte must not dodge the limit
		{"two\nlines", "one line"},
		{"   ", "empty"},
		{"bad \xff utf8", "UTF-8"},
	} {
		if r := memoIn(t, dir, "note", tc.text); r.code != 1 || !strings.Contains(r.err, tc.want) {
			t.Errorf("note %q: %+v, want error with %q", tc.text, r, tc.want)
		}
	}
	if r := memoIn(t, dir, "wake"); !strings.Contains(r.out, "No memories yet") {
		t.Fatalf("a rejected note was stored: %s", r.out)
	}
}

// Notes, summaries and regexes are free text: one that starts with a dash is text, not a flag. Only a lone -h asks for help.
func TestFreeTextIsNotFlags(t *testing.T) {
	dir := newMemory(t)
	for _, text := range []string{"- fixed the flaky test", "--verbose was the culprit"} {
		if r := memoIn(t, dir, "note", text); r.code != 0 {
			t.Fatalf("note %q: %+v", text, r)
		}
	}
	if r := memoIn(t, dir, "nap", "0-1", "- two fixes"); r.code != 0 || !strings.Contains(r.out, "0-1 saved") {
		t.Fatalf("a dashed summary: %+v", r)
	}
	if r := memoIn(t, dir, "recall", "--verbose"); !strings.Contains(r.out, "1 match.") {
		t.Fatalf("a dashed regex: %+v", r)
	}
	if r := memoIn(t, dir, "note", "-h"); r.code != 0 || !strings.Contains(r.out, "Usage:") {
		t.Fatalf("note -h did not print help: %+v", r)
	}
	if r := memoIn(t, dir, "recall", "."); !strings.Contains(r.out, "2 matches.") {
		t.Fatalf("note -h stored a memory: %s", r.out)
	}
}

// Multi-byte characters must not shift any later record's boundary.
func TestUTF8KeepsRecordsAligned(t *testing.T) {
	dir := newMemory(t)
	memoIn(t, dir, "note", "reunião com João em São Paulo: ação aprovada → coração tranquilo")
	memoIn(t, dir, "note", "a plain ascii memory right after the accented one")
	if r := memoIn(t, dir, "recall", "CORAÇÃO"); !strings.Contains(r.out, "João") {
		t.Fatalf("case-insensitive recall lost the accented memory: %+v", r)
	}
	if r := memoIn(t, dir, "recall", "plain ascii"); !strings.Contains(r.out, "#1 ") {
		t.Fatalf("the record after a multi-byte one reads shifted: %+v", r)
	}
}

// ---------------------------------------------------------------- lifecycle

// A long life, end to end: import, the refusal to wake, the nap chain, the paginated wake, recall, and every printed command is an order.
func TestLifecycle(t *testing.T) {
	const N = 400
	dir := newMemory(t)
	importDays(t, dir, N, longMemory)

	r := memoIn(t, dir, "wake")
	if r.code != 1 || !strings.Contains(r.out, "Cannot wake") || !strings.Contains(r.out, "wake again") {
		t.Fatalf("wake must refuse while its summaries are pending: %+v", r)
	}
	if !strings.Contains(r.out, "Run: memo nap ") {
		t.Fatalf("the refusal must order the nap: %s", r.out)
	}

	if n := settle(t, dir, joinBody); n != buildable(N) {
		t.Fatalf("did %d naps, %d blocks are buildable", n, buildable(N))
	}

	// Every part fits every harness, and together they are the whole budget.
	var lines []string
	k := 1
	for ; ; k++ {
		r := memoIn(t, dir, "wake", strconv.Itoa(k))
		if r.code != 0 {
			break
		}
		if len(r.out) >= capChars || strings.Count(r.out, "\n") >= capLines {
			t.Fatalf("part %d is %d bytes, %d lines", k, len(r.out), strings.Count(r.out, "\n"))
		}
		if strings.Contains(r.out, "Not awake yet") && !strings.Contains(r.out, fmt.Sprintf("Run: memo wake %d %d", k+1, N)) {
			t.Fatalf("part %d does not order the next part:\n%s", k, r.out)
		}
		for l := range strings.SplitSeq(r.out, "\n") {
			if strings.HasPrefix(l, "#") {
				lines = append(lines, l)
			}
		}
	}
	parts := k - 1
	if parts < 2 {
		t.Fatalf("a %d-line memory fit in %d part", resolve(nil)["WAKE_LINES"], parts)
	}
	if len(lines) != resolve(nil)["WAKE_LINES"] {
		t.Fatalf("woke with %d lines, want %d", len(lines), resolve(nil)["WAKE_LINES"])
	}
	if !strings.HasPrefix(lines[0], "#0-") || !strings.HasPrefix(lines[len(lines)-1], fmt.Sprintf("#%d ", N-1)) {
		t.Fatalf("want oldest summarized and newest raw: %q ... %q", lines[0], lines[len(lines)-1])
	}
	if r := memoIn(t, dir, "wake", strconv.Itoa(parts)); !strings.HasSuffix(r.out, "You are awake.\n") {
		t.Fatalf("the last part must end awake:\n%s", r.out)
	}

	// Recall matches the whole line -- id and date included -- and reaches memories the summaries lost.
	if r := memoIn(t, dir, "recall", "memory number 7,"); !strings.Contains(r.out, "#7 ") || !strings.Contains(r.out, "1 match.") {
		t.Fatalf("recall by text: %s", r.out)
	}
	if r := memoIn(t, dir, "recall", "^#7 "); !strings.Contains(r.out, "memory number 7,") {
		t.Fatalf("recall by id: %s", r.out)
	}
	if r := memoIn(t, dir, "recall", "2020-01-02"); !strings.Contains(r.out, "5 matches.") {
		t.Fatalf("recall by date: %s", r.out)
	}
	if r := memoIn(t, dir, "recall", "memory number"); len(r.out) >= capChars || !strings.Contains(r.out, "Narrow the regex") {
		t.Fatalf("a vague recall returned %d bytes", len(r.out))
	}

	// A nap when nothing is pending writes nothing and says so.
	before := fingerprint(t, dir)
	if r := memoIn(t, dir, "nap", "0-1", "overwrite"); r.code != 0 || !strings.Contains(r.out, "Nothing left") {
		t.Fatalf("nap with nothing pending: %+v", r)
	}
	if !maps.Equal(fingerprint(t, dir), before) {
		t.Fatal("a nap with nothing pending wrote something")
	}
}

// Zoom opens one node into its two halves, so halving from the root reaches any memory in log2 steps.
func TestZoomNavigates(t *testing.T) {
	dir := newMemory(t)
	importDays(t, dir, 257, longMemory)
	settle(t, dir, joinBody)

	// A half is printed by its block id, or by its memory's id once single.
	id := func(b block) string {
		if b.size() == 1 {
			return strconv.Itoa(b.lo)
		}
		return b.String()
	}
	target, b, calls := 177, block{0, 256}, 0
	for b.size() > 1 {
		r := memoIn(t, dir, "zoom", b.String())
		h := b.halves()
		lines := strings.Split(strings.TrimSuffix(r.out, "\n"), "\n")
		if r.code != 0 || len(lines) != 2 ||
			!strings.HasPrefix(lines[0], "#"+id(h[0])+" ") || !strings.HasPrefix(lines[1], "#"+id(h[1])+" ") {
			t.Fatalf("zoom %s is not its two halves: %+v", b, r)
		}
		b = h[0]
		if target >= h[1].lo {
			b = h[1]
		}
		calls++
	}
	if b.lo != target || calls != 8 {
		t.Fatalf("halving 256 memories took %d calls and landed on #%d", calls, b.lo)
	}
	if r := memoIn(t, dir, "zoom", "176-177"); !strings.Contains(r.out, "memory number 177,") {
		t.Fatalf("the last zoom must print the raw memories: %s", r.out)
	}

	if r := memoIn(t, dir, "zoom", "256-257"); !strings.HasPrefix(r.out, "#256 ") || strings.Count(r.out, "\n") != 1 {
		t.Fatalf("a half beyond the newest memory must be omitted: %q", r.out)
	}
	memoIn(t, dir, "forget", "128-255")
	if r := memoIn(t, dir, "zoom", "0-255"); !strings.Contains(r.out, "#128-255 not compressed yet") {
		t.Fatalf("zoom hid a missing summary: %s", r.out)
	}
	for _, bad := range [][]string{{"3-9"}, {"9-3"}, {}, {"1048576-2097151"}} {
		if r := memoIn(t, dir, append([]string{"zoom"}, bad...)...); r.code != 1 {
			t.Errorf("zoom %v succeeded", bad)
		}
	}
}

// Forget drops a summary and everything built on it, never the log; the naps it causes rebuild exactly what was dropped.
func TestForgetRebuilds(t *testing.T) {
	dir := newMemory(t)
	importDays(t, dir, 200, longMemory)
	settle(t, dir, constant("settled"))
	before := fingerprint(t, dir)
	log := before[filepath.Join(dir, "LOG.txt")]

	r := memoIn(t, dir, "forget", "16-31")
	if r.code != 0 || !strings.Contains(r.out, "from 16-31 up") {
		t.Fatalf("forget: %+v", r)
	}
	if fingerprint(t, dir)[filepath.Join(dir, "LOG.txt")] != log {
		t.Fatal("forget touched the log")
	}
	if r := memoIn(t, dir, "nap"); !strings.Contains(r.out, "Compress memories #16-31 ") {
		t.Fatalf("the forgotten block is not the next nap:\n%s", r.out)
	}
	// Resubmitting a settled block (two sessions paid the same nap) is not an error; submitting one out of order is.
	if r := memoIn(t, dir, "nap", "0-1", "again"); r.code != 0 || !strings.Contains(r.out, "already settled") {
		t.Fatalf("resubmitting a settled block: %+v", r)
	}
	if r := memoIn(t, dir, "nap", "0-31", "early"); r.code != 1 || !strings.Contains(r.err, "wrong block") {
		t.Fatalf("an out-of-order block: %+v", r)
	}
	if settle(t, dir, constant("settled")) == 0 {
		t.Fatal("forget created no work")
	}
	if !maps.Equal(fingerprint(t, dir), before) {
		t.Fatal("the rebuilt memory differs from the original")
	}
	for _, bad := range []string{"17-32", "1048576-1048577"} {
		if memoIn(t, dir, "forget", bad).code != 1 {
			t.Errorf("forget %s succeeded", bad)
		}
	}
}

// A part is rendered as of T, so a note (and the naps it causes) landing between two parts cannot change a part already promised.
func TestWakeAsOfSnapshot(t *testing.T) {
	dir := newMemory(t)
	importDays(t, dir, 300, longMemory)
	settle(t, dir, joinBody)
	before := memoIn(t, dir, "wake", "1", "300")
	if before.code != 0 {
		t.Fatalf("wake as of T: %+v", before)
	}
	for i := range 24 {
		memoIn(t, dir, "note", fmt.Sprintf("a note that lands mid-wake %d", i))
		settle(t, dir, constant("settled mid-wake"))
	}
	if r := memoIn(t, dir, "wake", "1", "300"); r != before {
		t.Fatalf("notes between parts changed a part:\n%s\n%s", before.out, r.out+r.err)
	}
	if memoIn(t, dir, "wake", "1", "999").code != 1 {
		t.Fatal("wake accepted a future T")
	}
}

// ---------------------------------------------------------------- config

func TestConfig(t *testing.T) {
	dir := newMemory(t)
	importDays(t, dir, 300, longMemory)
	settle(t, dir, constant("settled"))

	if r := memoIn(t, dir, "config", "WAKE_LINES=12"); !strings.Contains(r.out, "(default 96)") {
		t.Fatalf("config did not set: %s", r.out)
	}
	if n := strings.Count(memoIn(t, dir, "wake").out, "\n"); n > 13 {
		t.Fatalf("wake printed %d lines with WAKE_LINES=12", n)
	}
	if r := memoIn(t, dir, "config", "WAKE_LINES="); strings.Contains(r.out, "default") {
		t.Fatalf("an empty value did not restore the default: %s", r.out)
	}
	if n := strings.Count(memoIn(t, dir, "wake").out, "\n"); n <= 13 {
		t.Fatal("the default did not come back")
	}
	for _, bad := range []string{"WAKE_LINES=0", "WAKE_LINES=x", "WAKE_LINES=-1", "ENTRY_CHARS=999", "NOPE=1", "WAKE_LINES"} {
		if memoIn(t, dir, "config", bad).code != 1 {
			t.Errorf("config accepted %s", bad)
		}
	}

	// A typo written by hand must name the file and line, or nothing can recover from it: `memo config` cannot fix a file it refuses to read.
	scribble(t, filepath.Join(dir, "config"), os.O_APPEND, "WAKE_LNES = 100\n")
	for _, cmd := range []string{"wake", "config"} {
		if r := memoIn(t, dir, cmd); r.code != 1 || !strings.Contains(r.err, "config line") || !strings.Contains(r.err, "WAKE_LNES") {
			t.Errorf("%s with a bad config: %+v", cmd, r)
		}
	}
}

// ---------------------------------------------------------------- import

func TestImportRejects(t *testing.T) {
	dir := newMemory(t)
	for _, tc := range []struct{ data, want string }{
		{"2027-99-99 an impossible date\n", "not a real date"},
		{"2027-02-30 an impossible date\n", "not a real date"},
		{"yesterday something\n", "expected 'YYYY-MM-DD <text>'"},
		{"2027-01-02 later\n2027-01-01 earlier\n", "precedes"},
		{"2027-01-01 caf\xe9 in latin-1\n", "not UTF-8"},
		{"\n\n", "no memories"},
	} {
		p := filepath.Join(t.TempDir(), "in.txt")
		if err := os.WriteFile(p, []byte(tc.data), 0o644); err != nil {
			t.Fatal(err)
		}
		if r := memoIn(t, dir, "import", p); r.code != 1 || !strings.Contains(r.err, tc.want) {
			t.Errorf("import %q: %+v, want %q", tc.data, r, tc.want)
		}
	}
	if T := len(fingerprint(t, dir)[filepath.Join(dir, "LOG.txt")]); T != 0 {
		t.Fatal("a rejected import wrote memories")
	}
}

// ---------------------------------------------------------------- damage

// A crash mid-append leaves a partial record; the next append must drop it, or every later record is misaligned forever.
func TestTornWriteIsRepaired(t *testing.T) {
	dir := newMemory(t)
	memoIn(t, dir, "note", "before the power cut")
	scribble(t, filepath.Join(dir, "LOG.txt"), os.O_APPEND, "#1 2026-01-01 a half-written record")
	if r := memoIn(t, dir, "note", "after the power cut"); r.code != 0 || !strings.Contains(r.out, "Saved as #1.") {
		t.Fatalf("note after a torn write: %+v", r)
	}
	if fi, err := os.Stat(filepath.Join(dir, "LOG.txt")); err != nil || fi.Size()%logRec != 0 {
		t.Fatalf("LOG.txt left misaligned after a torn write: %v %v", fi.Size(), err)
	}
	if r := memoIn(t, dir, "recall", `^#1 [0-9-]{10} after the power cut$`); !strings.Contains(r.out, "1 match.") {
		t.Fatalf("the memory after a torn write reads wrong: %s", r.out)
	}
}

// A blank or undecodable summary is work nap cannot see: every command that reads one must name the one exit, forget.
func TestCorruptSummaryPointsAtForget(t *testing.T) {
	for name, damage := range map[string]string{
		"blank":        strings.Repeat(" ", 287) + "\n",
		"invalid utf8": "\xff\xfe corrupt bytes",
	} {
		dir := newMemory(t)
		importDays(t, dir, 32, longMemory)
		settle(t, dir, constant("settled"))
		memoIn(t, dir, "config", "WAKE_LINES=2") // wake reads 0-15 and 16-31
		scribble(t, filepath.Join(dir, "TREE", "16"), 0, damage)
		if r := memoIn(t, dir, "wake"); r.code != 1 || !strings.Contains(r.err, "forget 0-15") {
			t.Errorf("%s: wake over a corrupt summary: %+v", name, r)
		}
		// 0-31 compresses from its halves, so its nap must read 0-15.
		memoIn(t, dir, "forget", "0-31")
		if r := memoIn(t, dir, "nap"); r.code != 1 || !strings.Contains(r.err, "forget 0-15") {
			t.Errorf("%s: nap over a corrupt half: %+v", name, r)
		}
	}
}

// An unreadable level is a filesystem failure and must surface as one -- reading it as "not compressed yet" offers work that cannot be done.
func TestUnreadableLevelIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := newMemory(t)
	for i := range 4 {
		memoIn(t, dir, "note", fmt.Sprintf("memory %d", i))
	}
	settle(t, dir, constant("settled"))
	memoIn(t, dir, "config", "WAKE_LINES=2")
	level := filepath.Join(dir, "TREE", "2") // wake reads 0-1 and 2-3
	if err := os.Chmod(level, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(level, 0o644); err != nil {
			t.Error(err)
		}
	})
	r := memoIn(t, dir, "wake")
	if r.code != 1 || !strings.Contains(r.err, "permission denied") || strings.Contains(r.out, "not compressed") {
		t.Fatalf("an unreadable level: %+v", r)
	}
}

// ---------------------------------------------------------------- processes

// Parallel sessions note at the same moment; the lock must hand each a distinct id.
func TestParallelNotesGetDistinctIDs(t *testing.T) {
	bin := buildMemo(t)
	dir := newMemory(t)
	const P = 16
	cmds := make([]*exec.Cmd, P)
	for i := range cmds {
		cmds[i] = exec.Command(bin, "note", fmt.Sprintf("parallel note %d", i))
		cmds[i].Env = append(os.Environ(), "MEMORY_DIR="+dir)
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	for i := range P {
		r := memoIn(t, dir, "recall", fmt.Sprintf("parallel note %d$", i))
		if !strings.Contains(r.out, "1 match.") {
			t.Fatalf("note %d did not survive exactly once: %s", i, r.out)
		}
		if r := memoIn(t, dir, "recall", fmt.Sprintf("^#%d ", i)); !strings.Contains(r.out, "1 match.") {
			t.Fatalf("id #%d was not handed out exactly once: %s", i, r.out)
		}
	}
}

// Every command the tool prints must RUN on the machine that printed it, even with nothing on PATH.
func TestPrintedOrdersRun(t *testing.T) {
	bin := buildMemo(t)
	dir := newMemory(t)
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "MEMORY_DIR=" + dir}
	memo := func(args ...string) string {
		c := exec.Command(bin, args...)
		c.Env = env
		out, err := c.Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(out)
	}
	memo("note", "the first thing that happened")
	out := memo("note", "the second thing that happened")
	var order string
	for l := range strings.SplitSeq(out, "\n") {
		if s, ok := strings.CutPrefix(l, "Run: "); ok {
			order = s
		}
	}
	if order == "" {
		t.Fatalf("note did not order a compression:\n%s", out)
	}
	sh := exec.Command("/bin/sh", "-c", strings.Replace(order, `"<your line>"`, `"both things"`, 1))
	sh.Env = env
	if got, err := sh.CombinedOutput(); err != nil || !strings.Contains(string(got), "saved") {
		t.Fatalf("the printed order %q did not run: %v\n%s", order, err, got)
	}
}
