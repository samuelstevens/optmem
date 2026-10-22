# optmem

A Go port of [OptMem](https://github.com/VictorTaelin/OptMem): permanent, append-only memory for AI agents. The files on disk are upstream's, byte for byte, so either tool can open the other's memory. See the upstream README for how it works.

## Install

```sh
GOBIN=~/.local/bin go install github.com/samuelstevens/optmem/cmd/memo@latest
memo init              # creates ~/.local/share/optmem, prints the block for AGENTS.md
```

Paste the printed `## Memory` block into your agent's instructions file. To update, run the same `go install` again; `memo --version` names the commit installed.

## Differences from upstream

- The memory lives in `$XDG_DATA_HOME/optmem` (default `~/.local/share/optmem`), not `~/.optmem/memory`. `$MEMORY_DIR` still overrides it.
- Help and error text differ; the prompts agents act on (wake, nap) are upstream's.
- `recall` takes Go (RE2) regular expressions: no backreferences or lookaround.
- No Windows support: locking uses `flock`.

## Development

```sh
just install           # install this checkout to ~/.local/bin/memo
just test-unit         # fmt, lint, tests
just test-fuzz         # fuzz the cover and the recall merge
```
