# optmem

- I am the *only* user of this project. We never have to worry about changing command names, flags, or printed text. The one compatibility contract is the on-disk format (fixed-width `LOG.txt` and `TREE/<size>` records, `config`), shared with the Python tool at github.com/VictorTaelin/OptMem: either must open the other's memory.
- One package, cobra the only dependency. Files split by layer: `main.go` (CLI wiring, error reporting, where the memory lives), `tree.go` (the summary tree as pure math, no I/O), `store.go` (the on-disk format: records, lock, crash repair; everything that knows the bytes), `config.go` (sizes and `memo config`), `commands.go` (the other commands and the nap prompt).
- Agents act on the printed text, so a reworded prompt is a behavior change. List divergences from upstream in the README.

# Style

- Never hard-wrap comments or docs. A comment, a Markdown paragraph, or a list item is one line, however long; let the editor soft-wrap. Line breaks only where they carry meaning: code blocks, tables, separate paragraphs.

# Testing

Don't write echo tests. An "echo test" restates the implementation as an assertion. It passes if and only if the code does exactly what it currently does. These tests:

1. Break on any intentional change (false negatives).
2. Never catch bugs because they encode the same logic as the code.
3. Teach the reader nothing they couldn't learn faster by reading the source.

Examples of echo tests (don't write these):

```go
// Bad: just re-asserts defaults that are obvious from NewConfig.
func TestConfigDefaults(t *testing.T) {
	cfg := NewConfig()
	if cfg.Partition != "cpu" {
		t.Fatalf("Partition = %q, want cpu", cfg.Partition)
	}
	if cfg.LookbackHours != 4 {
		t.Fatalf("LookbackHours = %d, want 4", cfg.LookbackHours)
	}
}

// Bad: re-derives hours * 60 from the implementation.
func TestTimeConversion(t *testing.T) {
	params := Config{LookbackHours: 2}.ExecutorParameters("train")
	if params.TimeMinutes != 120 {
		t.Fatalf("TimeMinutes = %d, want 120", params.TimeMinutes)
	}
}

// Bad: tests that the standard library does what it says.
func TestSortInts(t *testing.T) {
	got := []int{2, 1}
	sort.Ints(got)
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("sort failed: %v", got)
	}
}
```

Test behavior that callers depend on, not implementation details you happen to know. Ask: "what would go wrong if this were broken?" If the answer is "nothing, someone would just update the test," it's an echo test.

- Prefer property-based tests. A single property test can prove a universal invariant across hundreds of random inputs. A unit test only proves one case. In Go, consider fuzz tests (`func FuzzXxx(*testing.F)`) when you can express the correctness condition as an invariant over many inputs.
- Don't test private APIs by default. In Go, this means prefer testing behavior through exported functions and command surfaces. If an unexported helper's behavior matters enough to test directly, it should usually be complex enough to justify that exception.
- Use flat package-level tests. Organize tests with `func TestXxx(t *testing.T)` and section-header comments. Avoid unnecessary test-suite frameworks or deeply nested subtests.
- Each test function should start with a short comment or have a clear name that describes the behavior or property being tested.
- Prefer not to use mocks or patching. Code should be written to either not need extensive testing if there is a required external dependency (maybe our code is a thin wrapper around `exec.Command`) or accept an interface/dependency if testing is absolutely necessary.
- Tests run commands in-process through `run`. Only behavior that needs real processes (the lock, printed commands running with nothing on PATH) builds the binary.
- The two unexported functions tested directly, `cover` and `matches.merge`, earn it: each is an invariant fuzzed over many inputs, and a bug in either is invisible at the command surface until a store is large.
