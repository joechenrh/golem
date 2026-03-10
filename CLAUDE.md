# Golem Development Conventions

## Go Version & Language Features

Target **Go 1.22+**. Use modern idioms:

- **`any` over `interface{}`** — always use `any` as the universal empty interface type.
- **Range-over-int** — use `for i := range n` instead of `for i := 0; i < n; i++` for simple counting loops. Only use C-style when the index is mutated inside the loop body or the step size is not 1.
- **`slices` package** — use `slices.Sort`, `slices.SortFunc`, `slices.Clone`, `slices.Contains` instead of `sort.Strings`, `sort.Slice`, manual `make+copy`, or hand-rolled search loops.
- **`maps` package** — use `maps.Copy` instead of manual map-copy loops.
- **`cmp` package** — use `cmp.Compare` inside `slices.SortFunc` comparators.
- **Builtin `min`/`max`** — use the Go 1.21+ builtins instead of custom helpers.
- **`strings.Contains`** — never hand-roll substring search; use the stdlib.

## Naming

- **camelCase** for all local variables, struct fields, and function names (Go convention).
- **PascalCase** for exported identifiers only.
- No snake_case except in test names (`Test_Some_Description` is acceptable) or where matching external API field names (e.g., JSON tags).

## Code Style

- Format all code with `gofmt` before committing.
- Keep functions focused — prefer small, single-purpose functions.
- Use `fmt.Errorf("context: %w", err)` for error wrapping.
- Prefer table-driven tests with `t.Run` subtests.
- Run `go vet ./...` and `go test -race ./...` before merging.

## Git Workflow

- **Commit each separate change** — when implementing multi-phase plans or touching multiple subsystems, commit each logical change separately with a clear message. Do not bundle unrelated changes into one commit.

## Documentation

- Design documents live in `design/` (13 files covering all major subsystems).
- When changing a subsystem's behavior, update the corresponding `design/*.md` file to keep docs in sync with the code.
