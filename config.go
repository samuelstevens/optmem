package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The sizes a memory may override in its own `config` file: the default, and what it means. Every harness truncates a command that prints too much, and each drops a different piece: Claude Code cuts the middle at 30,000 chars, pi cuts the head at 50 KB, Codex budgets 10,000 tokens. So the memory is handed over in parts that fit all of them. PART_* are transport limits, not memory limits.
type knob struct {
	name string
	def  int
	what string
}

var knobs = []knob{
	{"WAKE_LINES", 96, "the memory context: how many lines wake prints"},
	{"ENTRY_CHARS", 280, "the longest one memory may be, in bytes"},
	{"PART_CHARS", 20000, "output paging: largest part, in bytes"},
	{"PART_LINES", 500, "output paging: largest part, in lines"},
}

// resolve applies a memory's overrides to the defaults. A knob it does not set keeps the tool's default, so updating the tool still changes how it behaves.
func resolve(over map[string]int) map[string]int {
	out := map[string]int{}
	for _, k := range knobs {
		out[k.name] = k.def
	}
	maps.Copy(out, over)
	return out
}

// parseSize validates one knob, wherever it came from: the config file or argv. A bad knob stops every command, so the message has to say where it is written -- `memo config` cannot fix a file it also refuses to read.
func parseSize(name, v, where string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || strings.TrimLeft(v, "0123456789") != "" || n < 1 {
		return 0, fmt.Errorf("%s%s must be a positive whole number, not '%s'", where, name, v)
	}
	top := min(treeRec-8, logRec-40)
	if name == "ENTRY_CHARS" && n > top {
		return 0, fmt.Errorf("%sENTRY_CHARS is at most %d: a memory has to fit the fixed-width records", where, top)
	}
	return n, nil
}

func isKnob(name string) bool {
	return slices.ContainsFunc(knobs, func(k knob) bool { return k.name == name })
}

func knobNames() string {
	names := make([]string, len(knobs))
	for i, k := range knobs {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

// overrides is the knobs this memory sets for itself, read from its `config`.
func overrides(dir string) (map[string]int, error) {
	out := map[string]int{}
	p := filepath.Join(dir, "config")
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for n, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		k, v = strings.ToUpper(strings.TrimSpace(k)), strings.TrimSpace(v)
		where := fmt.Sprintf("%s line %d: ", pretty(p), n+1)
		if !isKnob(k) {
			return nil, fmt.Errorf("%s%s is not a size; delete the line, or name one of: %s", where, k, knobNames())
		}
		if out[k], err = parseSize(k, v, where); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeConfig rewrites `config`: every knob on its own line, commented out unless this memory overrides it.
func writeConfig(dir, me string, over map[string]int) error {
	var b strings.Builder
	b.WriteString("# OptMem sizes for this memory. A commented line means: follow the\n")
	fmt.Fprintf(&b, "# tool's default. Edit with `%s config NAME=VALUE`.\n\n", me)
	for _, k := range knobs {
		v, set := over[k.name]
		mark := "# "
		if set {
			mark = ""
		} else {
			v = k.def
		}
		fmt.Fprintf(&b, "%-2s%-12s = %-6d # %s\n", mark, k.name, v, k.what)
	}
	return os.WriteFile(filepath.Join(dir, "config"), []byte(b.String()), 0o666)
}

// cmdConfig shows this memory's sizes, or changes one. An empty value restores the default. Sizes only select what is printed, so changing one is free: no memory is touched and nothing is recomputed.
func cmdConfig(m *memo, args []string) error {
	over, err := overrides(m.dir)
	if err != nil {
		return err
	}
	for _, a := range args {
		k, v, eq := strings.Cut(a, "=")
		k, v = strings.ToUpper(strings.TrimSpace(k)), strings.TrimSpace(v)
		if !eq || !isKnob(k) {
			return fmt.Errorf("usage: %s config [NAME=VALUE ...]   # NAME one of %s", m.me, knobNames())
		}
		if v == "" {
			delete(over, k)
			continue
		}
		if over[k], err = parseSize(k, v, ""); err != nil {
			return err
		}
	}
	if len(args) > 0 {
		if err := writeConfig(m.dir, m.me, over); err != nil {
			return err
		}
	}
	for _, k := range knobs {
		v, set := over[k.name]
		note := ""
		if set {
			note = fmt.Sprintf(" (default %d)", k.def)
		} else {
			v = k.def
		}
		m.printf("%-12s %-7d %s%s\n", k.name, v, k.what, note)
	}
	return nil
}
