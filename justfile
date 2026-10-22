FUZZTIME := env_var_or_default("OPTMEM_FUZZTIME", "30s")

# Install this checkout, the same way `go install github.com/samuelstevens/optmem/cmd/memo@latest` installs main.
install:
    GOBIN="$HOME/.local/bin" go install ./cmd/memo

lint: fmt
    golangci-lint run
    go fix ./...

fmt:
    gofmt -w .

test-unit: lint
    go test ./...

test-fuzz:
    go test ./cmd/memo -run '^$' -fuzz FuzzCover -fuzztime={{FUZZTIME}}
    go test ./cmd/memo -run '^$' -fuzz FuzzMatchesMerge -fuzztime={{FUZZTIME}}

# Go test caches successful package test results; -count=1 disables that.
test-all: test-unit test-fuzz
    go test ./... -count=1
