# Contributing

Thanks for your interest in the Tech AI Knowledge Engine.

## Ground rules

- **Layered architecture.** Code imports downward only
  (`core → foundation → clients → repos → services → pipelines → workflows → transport`);
  no lateral imports between same-layer packages. `depguard` enforces the layer order; review
  catches lateral imports, which no lint rule checks.
- **Everything swappable is behind an interface, selected by configuration.** A new
  backend is a `Kind` + `Config` + `*_from_config` factory, chosen by a config value
  and injected at a composition root — not a compile-time import.
- **Tests.** Black-box, in the `go/tests/` and `python/techai_webutils/tests/` trees,
  asserting observable behaviour. Unit tests mock the external boundary (mockgen-generated
  mocks in Go, `unittest.mock` in Python); integration tests run the real dependency in a
  Docker container via testcontainers. No hand-rolled fakes.
- **Self-contained.** This repository never references anything outside itself: no
  consumer names, paths or design documents, and no issue/ADR/story ids in code, comments,
  docstrings, tests or config. Consumers may reference into it. Design rules live in
  [ARCHITECTURE.md](ARCHITECTURE.md); cite its sections (`ARCHITECTURE.md#<section>`).
- **No product code.** Product logic stays with its consumer; a consumer plugs its policy into
  a seam here (see [ARCHITECTURE.md](ARCHITECTURE.md#what-belongs-here)).

## Setup

Install [lefthook](https://github.com/evilmartians/lefthook), `gitleaks` and `markdownlint-cli2`,
then activate the Git hooks once per clone:

```sh
lefthook install
```

The hooks (`lefthook.yml`) check the commit message is a conventional commit
(`<type>(<scope>)?!?: <description>`, type one of `feat fix refactor docs test chore perf ci style`)
and, before each commit, scan the staged diff for secrets (`gitleaks`) and lint the staged
Markdown (`markdownlint-cli2`).

## Workflow

1. Fork and branch (`<type>/<short-description>`).
2. Make the change with a failing test first where behaviour changes.
3. Format Go code with `golangci-lint fmt ./go/...` (gofumpt, gci import sections,
   golines at 90 columns) and Python code with `uv run ruff format .`, then run the local
   checks below.
4. Open a PR with a conventional-commit title (`feat:`, `fix:`, `refactor:`, …).

## Local checks

CI runs the same commands; run them from the repository root unless noted.

Go:

```sh
go generate ./go/tests/mocks/...
go vet ./go/...
golangci-lint run ./go/...
golangci-lint fmt --diff ./go/...
go test -race ./go/...
INTEGRATION=1 go test -tags=integration ./go/...   # needs Docker
```

Python (from `python/techai_webutils`):

```sh
uv run ruff check .
uv run ruff format --check .
uv run basedpyright
uv run pytest -m "not integration"   # 90% coverage gate
uv run pytest -m integration --cov-fail-under=0   # needs Docker
```

Security scanners:

```sh
govulncheck ./go/...
uv tool run bandit==1.9.4 -c bandit.yaml -r python/techai_webutils/src
gitleaks git --redact --no-banner
osv-scanner scan source -r .
trufflehog filesystem . --results=verified --fail --no-update
# trivy scans the tracked tree only, so untracked local files are never reported
export_dir="$(mktemp -d)" && git archive HEAD | tar -xf - -C "$export_dir"
trivy fs --scanners vuln,secret --exit-code 1 --ignorefile .trivyignore.fs "$export_dir"
opengrep scan --error --config .semgrep/rules .
```

Docs:

```sh
markdownlint-cli2 "**/*.md"
```

The tool versions CI pins are in `.github/workflows/ci.yml`.

## Releases

Versioning follows the git tag as the single source of truth (SemVer). Maintainers
tag `vX.Y.Z`; the release workflow builds the GitHub release and publishes
`techai-webutils` to PyPI when its version is new. Record user-visible changes in
[CHANGELOG.md](CHANGELOG.md).
