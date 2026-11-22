# Go project guidelines

## Workflow

- Setup: run `go mod download`.
- Use only golangci-lint v2 for linting and formatting. Set `version: "2"` in `.golangci.yml`; run `golangci-lint run --fix` for lint fixes and `golangci-lint fmt` for formatting. Let these steps resolve LSP warnings such as unused imports.
- Build: `go build ./...`. Test: `go test ./...`. Race check: `go test -race ./...`.
- Before review, run `go mod tidy` and `golangci-lint fmt`, then summarize changes and list new dependencies.

## Code conventions

- Follow the existing layout. For new projects, ask whether to use a flat layout or `internal/`, `pkg/`, and `cmd/` before creating directories.
- Handle errors explicitly and document any use of `_`.
- Start each exported identifier’s doc comment with its name.
- Prefer `for i := range N` for index-based loops.
