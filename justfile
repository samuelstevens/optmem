FUZZTIME := env_var_or_default("OPTMEM_FUZZTIME", "30s")

install:
    go build -o "$HOME/.local/bin/memo" .

lint: fmt
    golangci-lint run
    go fix ./...

fmt:
    gofmt -w .

test-unit: lint
    go test ./...

test-fuzz:
    go test . -run '^$' -fuzz FuzzCover -fuzztime={{FUZZTIME}}
    go test . -run '^$' -fuzz FuzzMatchesMerge -fuzztime={{FUZZTIME}}

# Go test caches successful package test results; -count=1 disables that.
test-all: test-unit test-fuzz
    go test ./... -count=1
