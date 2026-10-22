// OptMem: a permanent, append-only memory for AI agents.
//
// A Go port of github.com/VictorTaelin/OptMem. The files on disk are byte for byte the ones the Python tool writes, so either tool can open the other's memory.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// errRefused exits 1 without a message: the reason is already on stdout.
var errRefused = errors.New("refused")

// run is the whole tool: args without the program name in, an exit status out. The command tree is built fresh for every run, because cobra commands keep state from parsing.
func run(me string, args []string, stdout, stderr io.Writer) int {
	m := &memo{dir: memoryDir(), me: me, sz: resolve(nil), out: stdout}
	root := &cobra.Command{
		Use:   "memo",
		Short: "OptMem: a permanent, append-only memory for AI agents.",
		Long: `OptMem: a permanent, append-only memory for AI agents.

The memories live in $XDG_DATA_HOME/optmem (~/.local/share/optmem), or in $MEMORY_DIR if set. See github.com/samuelstevens/optmem.`,
		SilenceErrors: true,
	}
	// The version Go stamps into the binary: a tag or a pseudo-version naming the exact commit.
	if info, ok := debug.ReadBuildInfo(); ok {
		root.Version = info.Main.Version
	}
	add := func(use, short string, args cobra.PositionalArgs, body func(*memo, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(c *cobra.Command, args []string) error {
			// With flag parsing off, cobra no longer sees -h itself.
			if c.DisableFlagParsing && len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return c.Help()
			}
			// The arguments passed; a usage dump would bury any later error.
			c.SilenceUsage = true
			return body(m, args)
		}}
		root.AddCommand(c)
		return c
	}
	// Every command but init runs against an existing memory. init is the one that creates it, so a typo in MEMORY_DIR is an error instead of a second, empty identity.
	opened := func(body func(*memo, []string) error) func(*memo, []string) error {
		return func(m *memo, args []string) error {
			if err := m.open(); err != nil {
				return err
			}
			return body(m, args)
		}
	}
	add("init", "Create this memory; print the setup block.", cobra.NoArgs, cmdInit)
	add("wake [part [T]]", "Read your memory. Run first, every session.", cobra.MaximumNArgs(2), opened(cmdWake))
	note := add(`note "..."`, "Record one memory: one short line.", cobra.ExactArgs(1), opened(cmdNote))
	nap := add(`nap [<lo>-<hi> "..."]`, "Do the pending compressions.", cobra.ArbitraryArgs, opened(cmdNap))
	recall := add("recall <regex>", "Search every memory ever recorded.", cobra.ExactArgs(1), opened(cmdRecall))
	add("zoom <lo>-<hi>", "Open a tree node: its two halves.", cobra.ExactArgs(1), opened(cmdZoom))
	add("forget <lo>-<hi>", "Drop a bad summary; nap rebuilds it.", cobra.ExactArgs(1), opened(cmdForget))
	add("config [NAME=N ...]", "Show this memory's sizes, or change one.", cobra.ArbitraryArgs, opened(cmdConfig))
	add("import <file>", "Bulk-load dated memories, 'YYYY-MM-DD <text>' per line (bootstrap only).", cobra.ExactArgs(1), opened(cmdImport))
	// Notes, summaries and regexes are free text: "- fixed it" is a note, not a flag.
	for _, c := range []*cobra.Command{note, nap, recall} {
		c.DisableFlagParsing = true
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()

	// The filesystem is the one thing the tool does not control: its failures are reported in the same voice as every other, naming the path, never as a stack trace.
	var pe *fs.PathError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errRefused):
	case errors.As(err, &pe):
		fmt.Fprintf(stderr, "%s: %s.\n", pretty(pe.Path), pe.Err)
	default:
		fmt.Fprintf(stderr, "Error: %s\n", err)
	}
	return 1
}

func main() {
	// Every command the tool prints has to RUN, even when nothing is on PATH, so it names itself by its full path.
	me := "memo"
	if p, err := os.Executable(); err == nil {
		me = pretty(p)
	}
	os.Exit(run(me, os.Args[1:], os.Stdout, os.Stderr))
}

// memoryDir is where the memory lives: $MEMORY_DIR if set, else the XDG data directory. The XDG spec says a relative $XDG_DATA_HOME is invalid and must be ignored.
func memoryDir() string {
	if d := os.Getenv("MEMORY_DIR"); d != "" {
		return expandUser(d)
	}
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "optmem")
	}
	return expandUser("~/.local/share/optmem")
}

func expandUser(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return home + p[1:]
}

// pretty is a path as the user would type it: symlinks kept, $HOME folded to ~.
func pretty(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
