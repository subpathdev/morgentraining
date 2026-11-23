# CLAUDE.md

Instructions for coding agents in **morgentraining**: the family morning-workout service (Go, page and API, SQLite with versioned plans, Helm chart in `charts/`).

- Package map and the layering rules as applied here: README → **Code layout**. Keep it current.
- The API contract is `api/openapi.yaml`; change the spec first, then regenerate `internal/httpapi/oapi`.
- UI text is German (with English plan texts); everything technical is English.

## Go conventions

Jan reads this code as production Go, even for a home project. Follow these from the first commit; don't argue against them with Go proverbs ("accept interfaces, return structs", "changing interfaces breaks implementers"). Explain a trade-off only when it is concrete.

### Layers

- **Domain first.** One domain package holds the types, the rules and the use cases (a `Service` with methods). It declares the interfaces (ports) it needs and knows nothing about HTTP, files, SQL or external APIs.
- **Adapters** (storage, file formats, external APIs) implement the domain's ports.
- **Transports** (HTTP, MCP, CLI) depend only on domain interfaces.
- **Thin `main`:** an `app` struct holds the parts; commands are its methods.

### Types and interfaces

- **Each layer owns its types and maps them explicitly** at every boundary: wire types, DB rows/columns, file formats. Never pass storage or generated types across a layer, and never `json.Marshal` domain types into a column.
- **Interfaces between layers**, defined by the consumer and kept small. A new capability gets a new or composed interface instead of a bigger one.
- **Exported interface, unexported implementation:** `type DB interface{…}`, `type db struct{…}`, `func Open(…) (DB, error)`.
- **Values that carry a rule or a secret** (IDs, names, keys, codes, tokens) are an exported interface over an unexported struct, created only by a validating constructor (`Parse…`/`New…`), plus `Restore…` for trusted stored data. No bare `string` types callers can forge. Compare through methods, not `==` on interface values.
- **Methods on types**, not free functions that all take the same dependency.
- **Typed values** (dates, enums, durations) instead of strings and ints; inject a clock instead of calling `time.Now()` in the domain.
- **Generated code** lives in its own package and is never edited by hand. HTTP APIs are spec-first (OpenAPI + oapi-codegen) where the repo has a spec.

### Tests and verification

- Tests per layer: table tests for rules, handlers against fakes, storage against the real thing (SQLite file, temp dir), one end-to-end test.
- Before handing over: `go build ./...`, `go vet ./...`, `go test -race ./...`, and `golangci-lint run` if the repo has a config. Run the service locally once.
- Report what was verified and what was not. Keep the README's code layout section current when packages change.

### Working rules

- Technical text (code, comments, commits, PRs, docs) in English, even for a German UI.
- Don't commit, push or merge without Jan's OK.
