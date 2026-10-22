package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Records are FIXED WIDTH, so a memory or a block is found by seeking to its offset -- no scanning, no index file to keep in sync. Position IS identity: memory i lives at i*logRec of LOG.txt, and block [k*s,(k+1)*s) lives at k*treeRec of TREE/<s>. Padding costs ~2x on disk and buys O(1) everywhere.
const (
	logRec  = 320
	treeRec = 288
)

// memo is one open memory and the context every command runs in.
type memo struct {
	dir string         // the memory directory
	me  string         // how this tool names itself in every command it prints
	sz  map[string]int // the sizes this memory runs with, by knob name
	out io.Writer      // where commands print
}

func (m *memo) printf(format string, a ...any) { fmt.Fprintf(m.out, format, a...) }

// open readies an existing memory. The directory is only ever created by `memo init`: creating it IS creating the identity, and that is a deliberate act. If any other command created it, a typo in MEMORY_DIR would silently open an empty store, and the agent would wake with no past and write a second identity.
func (m *memo) open() error {
	if fi, err := os.Stat(m.dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("no memory at %s; create one with `%s init`, or point MEMORY_DIR at an existing one", m.dir, m.me)
	}
	if err := os.MkdirAll(filepath.Join(m.dir, "TREE"), 0o777); err != nil {
		return err
	}
	if err := touch(m.logPath()); err != nil {
		return err
	}
	over, err := overrides(m.dir)
	if err != nil {
		return err
	}
	m.sz = resolve(over)
	return nil
}

func touch(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	return f.Close()
}

func (m *memo) logPath() string { return filepath.Join(m.dir, "LOG.txt") }

func (m *memo) treePath(size int) string {
	return filepath.Join(m.dir, "TREE", strconv.Itoa(size))
}

// count is how many whole records a file holds. A missing file holds none; any other failure is real and must surface.
func count(path string, rec int) (int, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(fi.Size()) / rec, nil
}

func (m *memo) logLen() (int, error) { return count(m.logPath(), logRec) }

// repair drops a partial trailing record left by a crash. It was never acknowledged. Without this the next append lands at a wrong offset and every later record is misaligned. Callers hold the lock.
func repair(path string, rec int) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if n := fi.Size(); n%int64(rec) != 0 {
		return os.Truncate(path, n-n%int64(rec))
	}
	return nil
}

// entry is one memory, as LOG.txt holds it.
type entry struct {
	id         int
	date, text string
}

func (e entry) String() string { return "#" + strconv.Itoa(e.id) + " " + e.date + " " + e.text }

// decode reads one log record. Records are sliced as BYTES and decoded one by one -- slicing decoded text would shift every boundary after the first multi-byte character.
func (m *memo) decode(i int, rec []byte) (entry, error) {
	corrupt := fmt.Errorf("memory #%d in %s is corrupt", i, pretty(m.logPath()))
	if !utf8.Valid(rec) {
		return entry{}, corrupt
	}
	line := unpad(rec)
	head, rest, _ := strings.Cut(line, " ")
	date, text, _ := strings.Cut(rest, " ")
	id, err := strconv.Atoi(strings.TrimPrefix(head, "#"))
	if err != nil {
		return entry{}, corrupt
	}
	return entry{id, date, text}, nil
}

// logSlice is memories [lo,hi), in one read.
func (m *memo) logSlice(lo, hi int) ([]entry, error) {
	f, err := os.Open(m.logPath())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, (hi-lo)*logRec)
	n, err := f.ReadAt(buf, int64(lo)*logRec)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	out := make([]entry, 0, n/logRec)
	for i := range n / logRec {
		e, err := m.decode(lo+i, buf[i*logRec:(i+1)*logRec])
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// logGet is memory i, in one seek.
func (m *memo) logGet(i int) (entry, error) {
	es, err := m.logSlice(i, i+1)
	if err != nil {
		return entry{}, err
	}
	if len(es) == 0 {
		return entry{}, fmt.Errorf("no memory #%d", i)
	}
	return es[0], nil
}

// treeGet is the summary of a block, in one seek; false if not built yet.
func (m *memo) treeGet(b block) (string, bool, error) {
	f, err := os.Open(m.treePath(b.size()))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil // not built yet; any other failure is real
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	rec := make([]byte, treeRec)
	n, err := f.ReadAt(rec, int64(b.lo/b.size())*treeRec)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	if !utf8.Valid(rec[:n]) {
		return "", false, fmt.Errorf("the summary of #%s is corrupt; run: %s forget %s", b, m.me, b)
	}
	s := unpad(rec[:n])
	return s, s != "", nil
}

// unpad is a record's text. Text never ends in whitespace (check strips it), so everything trailing is the padding pad wrote.
func unpad(rec []byte) string { return string(bytes.TrimRight(rec, " \n")) }

func pad(text string, rec int) ([]byte, error) {
	if len(text) > rec-1 {
		return nil, fmt.Errorf("too long: %d bytes; the record holds %d", len(text), rec-1)
	}
	return []byte(text + strings.Repeat(" ", rec-1-len(text)) + "\n"), nil
}

// lock takes the memory's exclusive lock; closing the file releases it. The lock file is opened for append, never truncated, so no holder is disturbed.
func (m *memo) lock() (*os.File, error) {
	p := filepath.Join(m.dir, ".lock")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, &fs.PathError{Op: "flock", Path: p, Err: err}
	}
	return f, nil
}

// appendSynced appends whole records to a file and waits for the disk.
func appendSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// logAppend appends memories: the only way LOG.txt ever changes. Ids are assigned INSIDE the lock: two sessions noting at the same moment must not be handed the same id. Returns the first id used.
func (m *memo) logAppend(items []entry) (int, error) {
	l, err := m.lock()
	if err != nil {
		return 0, err
	}
	defer l.Close()
	if err := repair(m.logPath(), logRec); err != nil {
		return 0, err
	}
	base, err := m.logLen()
	if err != nil {
		return 0, err
	}
	var data []byte
	for k, it := range items {
		it.id = base + k
		rec, err := pad(it.String(), logRec)
		if err != nil {
			return 0, err
		}
		data = append(data, rec...)
	}
	return base, appendSynced(m.logPath(), data)
}

// nextPending is the smallest block that can be built and has not been. Each level file holds a dense prefix, so its length says exactly how far that level got: this costs one stat per level, never a scan.
func (m *memo) nextPending(T int) (block, bool, error) {
	for size := 2; size <= T; size *= 2 {
		n, err := count(m.treePath(size), treeRec)
		if err != nil {
			return block{}, false, err
		}
		if n < T/size {
			return block{n * size, (n + 1) * size}, true, nil
		}
	}
	return block{}, false, nil
}

// pendingCount is how many blocks are buildable and not built. A level can hold MORE blocks than T needs -- T is a snapshot, and memories keep arriving while an agent reads -- so each level is clamped at zero.
func (m *memo) pendingCount(T int) (int, error) {
	total := 0
	for size := 2; size <= T; size *= 2 {
		n, err := count(m.treePath(size), treeRec)
		if err != nil {
			return 0, err
		}
		total += max(0, T/size-n)
	}
	return total, nil
}
