# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased]

## [0.0.24] - 2026-10-28

Python: unchanged (`techai-webutils` 0.6.1).

### Added

- Procedure `file` steps: `ensure` (create a missing file empty, replacing an empty
  directory in its place; a non-empty one fails), `copy` (seed `path` from `from` unless
  `path` exists), `mkdir` and `remove`, with an optional octal `mode`. Paths expand like
  a step's name and must stay inside `Deps.Root`; a dry run skips every file step.
  `Deps.FS` applies them (nil is the OS filesystem).

## [0.0.23] - 2026-10-28

### Added

- Match guard rows take `allow`: regular expressions (substrings under `literal`) that
  exempt a line from `forbid`, so a forbid pattern can spare a sanctioned literal RE2's
  missing lookahead cannot. `allow` expands `{placeholders}` like `forbid`, and a pattern
  that does not compile fails the guards' build.
- `foundation/workspace/golang.WorkspaceGraph` (over an `fs.FS`) and the I/O-free
  `ParseWorkspaceGraph` (over the go.work and go.mod bytes): every go.work module, the
  root included, with its module path and the workspace modules it requires directly,
  for module diagrams and impact analysis; `WorkUses` lists a go.work's use directories.

## [0.0.22] - 2026-10-28

Python: unchanged (`techai-webutils` 0.6.1).

### Added

- Guard `expr` rows read TOML and the time: `Codebase.Docs` decodes `.toml` files (format
  `toml`, and `decode('toml', text)`), an offset date-time as a timestamp and a local date
  or date-time as one in UTC; and an expression names `now`, the run's time as a
  timestamp, from the new `guard.Deps.Now` clock (nil is `time.Now`). An expiry in a
  suppression file is now a row, not a Go guard.
- Template generator rows render per element and into a marked region: `GenRow.Each`
  renders one file per element of the row's list data, with `out` and `template`
  themselves templates over the element (a Dockerfile per service, each from its
  language's template); `GenRow.Region` (`begin` / `end` marker text) replaces the lines
  from the begin marker through the end marker in the existing file at `out` and keeps
  the rest (a generated rule block inside a hand-kept linter config).
- Generator rows of kind `edit` (`types.GenEdit`) set values in an existing YAML file:
  `set` maps `yamlq` paths to value templates over the data (or, with `each`, the
  element), and each value replaces its token in place through `yamlq.Rewrite`, so
  comments, quoting and every other line are kept. A path that selects no scalar fails:
  the file must declare every value the row projects.
- `foundation/openapi`: the build-time half of the JSON:API runtime. It turns
  protoc-gen-openapiv2's Swagger 2.0 output into per-service OpenAPI 3 specs with JSON:API
  envelopes (`Convert`; the Swagger → OpenAPI 3 step is kin-openapi's `openapi2conv`, and
  each `$ref`'s description and readOnly are kept), builds the operation-keyed route table
  (`BuildRouteTable`) and writes it as the Go package the JSON:API runtime reads
  (`WriteRuntimeRouteTable`), and reports every REST RPC it cannot classify
  (`CheckCompleteness`). The `jsonapi.v1` file, message and method options drive it,
  read from a buf descriptor set. `Convert` takes the API-key header schemes every
  operation requires as data.
- `foundation/protogen`: the file passes a proto generation runs over buf's output. It
  adds the generated Python package's `__init__.py` files and PEP 561 `py.typed` marker,
  rewrites its absolute imports to package-relative ones, de-optionalises the TypeScript
  fields a descriptor marks required, reads a descriptor set, and cleans a generated
  directory or stashes it and settles it back after a regeneration.
- `foundation/mockgen`: a `Generator` over the `//go:generate mockgen` directives under a
  set of directories. It renders each mock into memory through the injected
  `CommandRunner`, so `gen.Check` compares the on-disk mocks with the render without
  writing to the tree.
- `foundation/workspace/golang` and `foundation/workspace/python`: workspace discovery
  over an `fs.FS` rooted at the repository. `golang` lists the go.work modules
  (`GoWorkModules`, with a filter), renders a scoped go.work that keeps the root's
  toolchain, godebug and replace directives with local replace targets made absolute
  (`ScopedWork`), reads the root module path (`ModulePath`), resolves staged files to
  their modules (`StagedModules`, with a keep filter), and probes for loadable packages
  through a per-run `Lister` that memoizes `go list` per directory. go.work and go.mod are
  parsed by `golang.org/x/mod/modfile`. `python` finds the directories holding a
  `pyproject.toml` under given roots (`Packages`) and a package's importable module
  (`ModuleName`).
- `foundation/imagescan`: a container-image vulnerability gate. `ScanImage` runs a Trivy
  image scan under a `TrivyPolicy` (scanners, severities, ignore file, ignore-unfixed,
  report-only) through the injected `CommandRunner`; a missing trivy is a Skip, or a
  Fail under strict. `GrypeEmergencies` / `CheckGrypeReport` read a grype JSON report
  and block only on a known-exploited (CISA KEV) vulnerability that has a fix.
- `impact.Classify` / `impact.SplitPaths`: classify changed paths into named regex
  classes (`impact.Class`, a language by its file types or a domain by its
  directories), one verdict per class in order; a pattern that does not compile is
  `CodeInvalidInput`. `SplitPaths` splits `git diff --name-only -z` output.
- `yamlq.Upsert` / `UpsertAt` splice two more cases instead of failing: a null on the
  path (`key:`, `key: ~`, `key: null`) becomes a mapping holding the missing keys, its
  inline comment kept; a source with no document (empty, or only comments) gains the
  keys as a block mapping after its lines.
- `yamlq.Append` / `AppendAt`: add an item to the end of a sequence. Into a non-empty block
  sequence the item is spliced after the last item, at the items' indentation, so comments
  and key order elsewhere are kept; a flow or empty sequence re-encodes.
- `KubectlClient.AuthCanI(ctx, verb, resource)`: `kubectl auth can-i` as a typed call.
  Exit 0 is allowed and exit 1 printing `no` is denied; any other failure (an unreachable
  cluster) is an error, never a denial. The stub denies everything.
- `clients/auth0`: an `Auth0Management` client family. `stub` (the default) reaches no
  tenant and is `CodeUnavailable`; `management` reads a tenant through the Management API
  (a client-credentials token minted once and reused until it nears expiry, the paged
  organization ids, an organization's id by name, an unknown name `CodeNotFound`) and its
  public JWKS (`SigningKey`: the first `sig` key's public key as PEM, with its kid, which
  needs no credentials). Credentials are `Deps`, never `Config`. `tests/mocks` generates
  `MockAuth0Management`.
- `transport/cobra`: `Options.Resolve(ctx, op)` returns the registry and observer one run
  uses, so a CLI that builds its registry after parsing (in a `PersistentPreRun`) builds the
  tree first; `Tree` then takes a nil registry, takes each leaf's flags from its spec, and
  an unknown operation fails the run. Every leaf (and every `Command`) carries its
  operation under `OperationAnnotation` (`"operation"`).
- `transport/cobra`: `Options.Before(cmd, op, input)` adjusts a leaf's input object and
  returns the operation to run (one a flag selects), and `Options.After(cmd, op, err)`
  runs after every run, failed or not, and returns the command's error.
- `core/types`: `ProcessSpec.LogFile` appends a process's stdout and stderr (those not
  given a writer) to a file the supervisor creates 0600, so a detached process's log needs
  no caller-side file handling.
- `tests/mocks`: `MockFS`, a generated mock of `interfaces.FS`.
- `clients/git`: `GitClient.RemoteHasRef(ctx, root, remote, ref)` asks a remote for a branch
  or tag (`git ls-remote --exit-code --heads --tags`); no match is `false`, a remote that
  cannot be asked is `CodeUnavailable`, and the stub reports `false`.
- `transport/cobra`: `Options.Run(ctx, reg, op, raw, obs)` runs each leaf's operation in
  place of the new `RunLocal` (the default, which admits secret flags): a scheduled
  front-end passes `transport/cron.Run`, so one tree serves the CLI and its CronJobs.
- `core/types`: `FlagStringArray` (`string_array`), a repeatable string list flag that never
  splits on commas, and `IsListFlag`; map inputs and their schemas treat it as a list.
- `services/procedure`: the `probe` step — `probe: {http | tcp | exec, status, contains,
  hint, timeout, delay, max_delay}` is a read-only readiness check retried with doubling
  waits (500ms → 5s by default) until it passes or its timeout ends; each attempt is
  bounded at 5s. An HTTP probe passes on `status` (any 2xx when unset) with `contains`
  in the first 4 KiB of the body, a TCP probe once a connection opens, an exec probe on
  exit 0 with `contains` in stdout. Exactly one kind must be non-empty once expanded, so
  one `each` row can probe a list of mixed checks; a failure carries the last attempt's
  error and the hint. `Deps.HTTP` / `Deps.Dialer` inject the HTTP client and dialer
  (nil: `http.DefaultClient`, a zero `net.Dialer`).
- `core/interfaces`: `Dialer` (`*net.Dialer`'s `DialContext`), mocked as `MockDialer`.
- `foundation/scaffold`: `PlanFiles(ctx, cb, files, policy)` plans content the caller
  rendered itself (a `types.Artifacts` map) under the same Once / Regenerate / Preserve
  decisions as `Plan`, in path order, for `Apply`.
- `services/procedure`: `Vars.Duration(key)` — the `time.Duration` a CLI flag carries or the
  text a tool call sends (`"90s"`); a missing, other or unparseable value is 0, like the
  other accessors.

- `core/types`: `FlagSpec.Secret` marks a credential flag. `ops.MapSchema` leaves it out
  of the input schema and `ops.Decode` refuses it, so a schema-driven caller (an MCP tool
  call, a CronJob's arguments) can neither see nor send it; `ops.DecodeLocal` (used by
  `transport/cobra`, where a human typed the flags) admits it.

### Changed

- **Breaking for implementers:** `interfaces.CommandRunner` gains
  `RunBufferedWithInput(ctx, dir, extraEnv, input, name, args...)`, a buffered run fed
  `input` on stdin (a manifest piped to `kubectl apply -f -` never touches disk);
  `foundation/system`'s runner, every builder decorator, the buffering runner and the
  gate's no-runner implement it, and `MockCommandRunner` is regenerated.
- `execution/ops`: `Decode` rejects a map input's key that no flag or argument declares,
  as it already did for a struct input's unknown field (`CodeInvalidInput` naming the
  key); null arguments decode as empty. `transport/mcp` decodes bounded and long-running
  tool calls through it, so an MCP caller's typo or secret argument fails the call
  instead of reaching the run.
- `services/procedure`: a registered Go step that panics fails its row instead of
  crashing the process — the engine wraps every Go step in `foundation/decorate`
  recovery, so the panic becomes a failed row named after the step (its message says the
  panic was recovered, never the panic value), the run stops as on any failed row, and
  `finally` still runs. `Deps.Log` receives the recovered panic with its stack; nil logs
  nothing.

### Fixed

- `execution/ops`: a map input's duration flag is a `time.Duration` (from JSON nanoseconds,
  a Go integer or its text, defaults included), so a procedure interpolates `{timeout}`
  as `2m0s` instead of `120000000000`.
- `transport/cobra`: a duration flag's spec default is its registered default, so help
  prints it and the flag no longer counts as set when not given.

## [0.0.21] - 2026-10-26

Python: unchanged (`techai-webutils` 0.6.1).

### Fixed

- `clients/outbox/sqs`: the sink packs each `SendMessageBatch` within SQS's 1 MiB request
  limit as well as its 10-entry limit. It used to pack by count alone, so ten large records
  made one oversized request that SQS rejected whole (`BatchRequestTooLong`) on every
  attempt until all ten parked. A record over SQS's 256 KiB message limit now fails
  `CodeInvalidInput` without reaching SQS, instead of failing every retry.

## [0.0.20] - 2026-10-24

Python: unchanged (`techai-webutils` 0.6.1).

### Added

- `services/procedure`: the `exec` step — `exec: ["{command...}"]` runs a command line the
  caller supplies (its first element the executable, no toolchain lookup), with the
  step's cwd, env (a `-NAME` entry removes an inherited variable), missing policy,
  stream, capture and dry-run handling; an empty command is `CodeInvalidInput`, and a
  step naming both `tool` and `exec` is refused at compile. This is what lets
  `search exec -- <cmd>` be a row.
- `services/procedure`: every procedure sees the platform it runs on as the built-in
  variables `os` and `arch` (`runtime.GOOS` / `GOARCH`), so a platform-specific row is a
  `when:` condition (`os == 'windows' && arch == 'arm64'`); an input of the same name
  overrides them, which is how a test pins a platform.
- `services/procedure`: `concurrent: true` on an `each` row runs its items concurrently
  (NumCPU-capped, `engine.Pool.FanOut`) — every item runs even when one fails, the results
  keep the items' order and the items' variables do not propagate, as in a `parallel`
  block; `concurrent` without `each` is rejected at compile. Text placeholders accept a
  path into a map-valued variable — `{item.key}` in a name, cwd, args or env, and
  `{item.list...}` splats — so a fan-out over records (build targets, modules) is rows.
- `services/guard`: the `go` kind — a row runs the Go guard the product registers in
  `Deps.Go` under its `func` (or its name), with the expanded row and placeholders; a
  `tool` names the binary it shells and `inputs` the files whose digest, with the tool's
  version (`Deps.Versions`) and the expansion, caches a pass in `Deps.Cache` — a cached
  expansion is skipped and reported `cached`, a failure is never cached. A row's
  expansions run in parallel (`engine.Pool.FanOut`), findings in expansion order, one
  progress tick per expansion. `Deps.RenderTool` names the renderer's binary, counted
  among a chart-rendering row's tools. `Guard.Meta().Tools` lists a row's tools.
- `services/suite`: `Composite` — a gate factory over a `types.Composite`: ordered phases
  that union suite references (each built by the product's `Suites` func under its own
  flags, selection and strictness) and named jobs from a `Jobs` map; an empty phase is
  dropped, an unknown job or a reference without a builder is one failing
  `INVALID_INPUT` job. `Entry.Tools` / `Step.Tools` and `Tools(suite, catalog, flags)`:
  the sorted, unique tools a suite's selection shells (a run row's `tool`, a guard's
  tools, a step's `Tools`) — what provisioning installs for a suite. Under
  `Opts.Strict`, a guard skipped for any reason but a cached pass fails with the skip's
  detail (`<tool> is not installed`).
- `types.Composite`, `types.CompositePhase`, `types.SuiteRef`, `types.GuardRow.Tool`,
  `types.GuardRow.Inputs`, `types.EntryMeta.Tools`, `types.CachedDetail` (the step
  detail of a cached skip, shared by the tool runner and the guards).
- `interfaces.Codebase.Docs`: the selected files decoded as documents
  (`types.Document{Path, Index, Format, Value}`): YAML and JSON natively, `.tf`/`.hcl`
  through hcl2json and `.alloy` through grafana/alloy's syntax; `codebase.Decode(format,
  name, src)` decodes one source, and the `expr` kind's CEL environment gains
  `decode(format, text)` for a River config embedded in a values file.
- `services/toolrun/selectors`: the `file_dirs` target kind — the distinct directories
  of the files a glob selects, one target each (a chart with a tests suite, a kustomize
  overlay).
- `foundation/stepcache.Digest(ctx, codebase, selector)`: the content digest of a file
  selection, the key the tool runner and the cached guards share; `Snapshot.Keys`.
- `clients/toolchain`: `InstallToolsOptions.DryRun` lists the tools an install would put
  in place and installs nothing; an archive asset's `member` names the binary's path
  inside the archive when it is not the binary's name.
- `services/procedure`: `sets` on a Go step names the variables it sets, so later
  expressions may read them (a step's outputs are declared like captures); a streamed
  tool step may `capture: {as: exit}` — the output streams, the exit status is captured;
  an exit capture records the real status (0, the command's, or 1 when it failed
  without one) and reports a non-zero status as a warning (`exit N`), never a failure,
  so a verdict step after it decides.
- `foundation/tfplan`: the saved-plan reader over hashicorp/terraform-json — `ReadUnits`
  walks a terragrunt `--json-out-dir` for each unit's `tfplan.json` into a `UnitPlan`
  (its changes, add/change/destroy counts — a replace is one add and one destroy —
  imports, output changes), `Summary` renders the reviewer's markdown (destroys first,
  then the unit table), `FailedSummary` names a failed plan.
- `foundation/ghactions`: `AppendSummary` appends markdown to a job summary file (an
  empty path is a no-op), `MaskEnv` registers every value of a `VAR=value` block with
  the runner's log masking (`::add-mask::`, one line per value line), `EscapeData`
  escapes a workflow-command value.
- `services/procedure`: a tool step's `env` expands like its `args` (`{name}`,
  `{name...}`, `${expr}` lists); `ProcStep.Stream` runs the tool with its output
  streamed to the operator (`RunWithEnv`) instead of buffered, and never captures.
- `services/procedure`: `Procedure.Finally` — steps that run after the steps whatever
  their outcome (a failure, an error, a cancellation), on a context detached from the
  run's cancellation and bounded by `finally_timeout` (`DefaultFinallyTimeout`, 30s),
  seeing the run's variables plus `failed` and `canceled`; a Finally step's error is its
  failed row and never stops the next one. `ProcStep.Fail` — an assertion step that
  runs nothing and fails with its expanded message (under `when`: a refused input as a
  row). Output a capture cannot decode (`capture json` on a non-JSON body) is the step's
  failed row, with the output as its detail, instead of an engine error.
- `services/procedure`: `Vars.Text` / `Bool` / `Strings` / `Int` read a step's variables
  typed, accepting the JSON-decoded forms a tool call sends and treating a missing or
  mistyped value as the zero value, so a Go step never hand-writes the type switches.

### Changed

- `services/procedure`: an advisory step's failure is reported as a warning (as the
  tool runner's `advisory` already did), not a failure that happens not to stop the
  procedure — a best-effort cleanup or a `pkill` that matched nothing no longer fails
  the gate.
- `foundation/schemagen.Reflect`: a recursive type (a step holding steps) reflects by
  reference — the type once under `$defs`, a `$ref` where it recurs — instead of
  overflowing the inlining reflector; `AllowExtraProps` walks `$defs` too.
- `ops.MapInput`: a run receives every declared input — an omitted flag as its default
  or its type's zero, an omitted argument as `""`, the rest as no items — and a sent
  value coerced to the flag's type (a JSON number to an int or duration, a JSON list to
  `[]string`), so a procedure's expressions see the same input from every front-end.

## [0.0.19] - 2026-10-23

Python: unchanged (`techai-webutils` 0.6.1).

### Added

- `report.Console`: a CLI's console on one stream: `Level` (`LevelQuiet`, `LevelNormal`,
  `LevelVerbose`, changeable after its reporters exist), a leveled `Logger`, `Debugf`,
  `Infof`, `Successf`, `Warnf`, `Errorf`,
  `CommandHook` (echoes each command at the verbose level), `FormatError` (prints
  `<CODE>: <message>`, the cause when verbose, and returns the exit code) and
  `Observer()` (table when verbose; line plus GitHub Actions annotations in CI; the
  interactive line otherwise; the GitHub step summary appended when
  `ConsoleConfig.StepSummaryPath` is set), and `Groupf` / `EndGroup` (a collapsible log
  group under GitHub Actions, otherwise a header when verbose). `ConsoleConfig` carries
  `Color`, `CI`, `GitHubActions`, `StepSummaryPath` and `Hints`; the console reads no
  environment.
- `report.ExitCode`: 2 for invalid input, not found and quality failed; 3 for
  unavailable; 1 otherwise.
- `report.ActionableLines` and `report.IsNoiseLine`: condense a failed step's output to
  the lines that explain it (Go test failures, file:line errors, pytest `FAILED`,
  govulncheck findings, the files a diff touches), dropping runner, test, tool, diff and
  govulncheck noise.
- `report.WriteStepSummary`: a gate's verdict as GitHub step-summary markdown.
- `report.Hint` and `Config.Hints`: remediation hints the failure block prints under a
  matching failed or warned step; `Config.Interactive` and `Config.Color`.
- `types.NewStep`, `types.OneStep`, `types.CheckStep`: single-step result builders.
- `errors.SplitCode`: splits an `AppError`'s `"<CODE>: <rest>"` text.
- `dataset.(*Dataset).Decode`: decodes every section into the matching field of one
  struct (yaml tag name, else the lower-cased field name), strictly; a key with no field
  or a field the section's type does not declare is `INVALID_INPUT` naming the file.
- `clients/prompt`: the confirmation `Prompter` on `client.Kinds`: `deny` (the default,
  refuses with the `--yes` guidance) and `tty` (asks on injected streams). Built
  undecorated, since a timeout or retry would cancel or repeat a question.
- `helm.BuildDeps`: runs `helm dependency build` once per chart per `codebase.Snapshot`,
  sharing the result with concurrent callers and with the `Renderer`.
- `infra.S3Config.SSEKMSKeyID` (`S3_SSE_KMS_KEY_ID`): every s3 `PutObject` and multipart
  create encrypts with that KMS key (`aws:kms`).
- `session.Expiring` and `session.ExpirySkew`: a cached credential that implements
  `Expiring` is resolved again once it is within five minutes of expiry.

- `clients/aws/account`: an environment's AWS session verified against the account it
  must run in, on `client.Kinds` (`stub`, the default, and `sdk`). The `Resolver` loads
  the ambient chain or the target's shared profile (`forceProfile` forces it), pins the
  retrieved credentials, asserts the account through the decorated `clients/aws/sts`
  client and caches the `Session` until it nears expiry; a wrong account is
  `INVALID_INPUT`, an unusable session `UNAUTHORIZED` naming the target's `LoginHint`.
  `UpsertProfile` writes an SSO profile into an owner-fenced block of the shared AWS
  config (refusing a same-named stanza outside it) and `ConfigPath` resolves that file.
- `secrets.GuardDomain` (refuses a credential whose domain is empty or not the expected
  one) and `secrets.ProjectEnv` (a secret's fields as sorted `VAR=value` child-env
  entries, skipping a `NOT_FOUND` field).
- `services/probe/readiness`: read-only `Probe[readiness.Target]`s that a cloud
  deployment's prerequisites are in place: `Secrets`, `DNS`, `SES`, `Bedrock`
  (`Target.NoInvoke` skips its billed call and warns `readiness.Unverified`), `Quotas`,
  `Registry`, `OIDC` and `Cluster`; `readiness.IsUnverified` reads such a warning.
- `clients/chaos`: the `FaultInjector` on `client.Kinds` (`stub`, the default, and
  `chaos-mesh`, which creates and deletes `PodChaos` / `NetworkChaos` resources with
  kubectl, labeled with `Config.ManagedBy`), plus `types.Fault`, `FaultTarget`,
  `FaultSpec` and the `FaultPodKill`, `FaultNetworkLatency` and `FaultNetworkLoss`
  types.
- `clients/planstore`: the saved-plan `PlanStore` on `client.Kinds` (`local`, the
  default, and `object`, which keeps each unit's plan in a `StorageClient` behind a
  SHA-256 manifest and refuses, on `Load`, another owner's manifest, a key outside the
  set's prefix, a mismatched digest and an expired object), over `types.PlanSet`; and
  `planstore.FindPlans`.
- `interfaces.HTTPDoer` and `interfaces.NSResolver`, and the generated mocks
  `MockHTTPDoer`, `MockNSResolver` and `MockFaultInjector`.
- `types.ToolRun.Group` (the step group; empty means the verb) and `ToolRun.Advisory` (a
  failed invocation is a warning that never fails the run), and a repo-relative `Cwd`
  (a run's fixed working directory under the root).
- `types.RunOptions` (`Vars` for `{name}` placeholders in a run's args, `Fix`, `Strict`)
  and `types.Selection`, with `toolrun.Select`: picks a suite's runs by verb, tags (all
  of, any of, none of) and name.
- `types.TargetSpec.Globs`, `Roots` and `Exclude`: extra file globs, and target filters
  every selector kind applies (keep targets under the roots, drop those matching an
  exclude glob).
- `stepcache.IsOff`: reports the off (or nil) cache. The tool runner computes no step
  key under it, so a cache-off run (CI) hashes no inputs and probes no tool version.
- The `empty_output` tool-run parser: any output is a failure (a formatter's list of
  unformatted files).
- `job.(Base).Check` (a one-step pass/fail job), `job.Warn` (downgrades a job's failed
  steps to warnings) and `job.Component` (a job's snake_case config component name).
- `system.(*Builder).WithEnv`: runs every command with extra environment ahead of the
  call's own.
- `toolchain.(*Installer).InstalledVersion`: the build of a tool on PATH (a Go binary's
  package and module version, else its version command's output), for keying cached
  tool-run results by the tool that produced them.

- `services/suite`: a suite gate from data. `types.Suite` (selectors, modifiers, gated
  flags, phases, opt-in steps) selects entries from a catalog (`Runs` for tool-run rows,
  `Steps` for registered Go steps, `Guards` for repository guards); `Select`, `Plan` and
  `Gate` run the selection phase by phase. `types.Selection.Matches` is the shared
  name/verb/tag match.
- `services/guard`: repository guards from data. `types.GuardRow` kinds `match` (line
  patterns, regular or `literal`, with per-file-type comment skipping and `names` for one
  guard per pattern), `yaml` (a path query with an optional expected value), `expr` (a
  CEL expression over `files`, `file`, `docs`, `objects` and `vars` returning a bool or a
  list of findings) and `go` (a product probe); `each` expands a row over a registered
  `Expander`. `interfaces.Guard` is a `Probe[Codebase]` with `Meta`.

- `services/procedure`: procedures from data. A `types.Procedure` (inputs, CEL-derived
  vars, steps) runs tool steps with `{name}` / `{name...}` / `${expr}` arguments and
  `capture` (`text`, `lines`, `json`, `exit`), `when` and `each` (CEL), `parallel`,
  `call`, `confirm` (through the `Prompter`; refusal is `CANCELED`) and registered Go
  `StepFunc`s; `advisory` failures continue. `interfaces.ProcedureRunner` runs one by
  name and `suite.Procedures` makes them suite entries. `RunOptions.DryRun` reports each
  `mutating` step's command without running it; `RunOptions.Yes` answers every
  confirmation.

- `transport/cobra.Tree`: a Cobra command tree from data. `types.CommandSpec` (use,
  help, aliases, hidden, help group, flags incl. persistent and required, positional
  arguments, subcommands) makes a group print its help and a leaf run its operation; a
  leaf's flags and arguments become a JSON object decoded into the operation's input.
- `ops.MapInput` / `ops.MapSchema`: an operation whose input is a `map[string]any`
  keyed by `types.FlagSpec` fields, its MCP schema derived from the same flag specs.

- `gen.NewFromRow`: generators as data. A `types.GenRow` of kind `run` runs a tool with
  `{out}` set to a scratch directory and returns every file written there under `Out`;
  kind `template` renders a repository `text/template` over a registered `gen.DataFunc`
  (missing keys fail). Both work with `gen.Check` and `gen.Apply`.
- `types.ToolRun.Env` and `types.MergeEnv`: per-run environment entries (`NAME=value`
  sets, `-NAME` removes an inherited variable, placeholders expand); the system runner
  and `clients/process/exec` apply removals.

### Changed

- `interfaces.ToolRunner.Run` takes a `types.RunOptions` in place of its `fix bool`.
  Migration: pass `types.RunOptions{Fix: fix}`.
- `helm/exec.(*Client).DependencyBuild` runs under a private repository cache and config,
  removed on return, so concurrent builds of different charts no longer race on helm's
  shared per-user index files.
- `report` line and table output: the line reporter prints a status line per phase, a
  failure block with each step's actionable lines and hint, and `"n ok, … (elapsed)"`;
  its per-step ticks need `Config.Interactive` (they were always on). The table reporter
  prints a `STEP / STATUS / DURATION / ERROR` table and a `TOTAL:` line per phase.
  `Config.Verbose` now selects the verbose level instead of printing every step's detail.
- The tool manifest's schema moved from `clients/toolchain` to `core/types`, so a
  foundation-tier manifest can hold it without importing a client. Migration: replace
  `toolchain.ToolEntry`, `InstallMethodEntry`, `AssetEntry`, `InstallKind` (and its
  `InstallKind*` values), `ToolCategory` (and `Category*`), `VersionParseFirst`,
  `VersionParseGo` and `ValidateToolEntries` with the same names in `types`.
- `storage/s3.NewClient` and `NewClientFromAPI` take the SSE-KMS key id as a new last
  argument. Migration: pass `""` for unencrypted writes.

### Fixed

- The tool runner checks a prefixed tool's prefix (`uv` for `uv run ruff`) for presence,
  not the tool's own binary, which need not be on PATH.
- `dataset`: a folded (`>`) scalar with more-indented lines decodes to the string the file
  holds; re-encoding a section had re-folded it with an extra blank line.

## [0.0.18] - 2026-10-21

Python: unchanged (`techai-webutils` 0.6.1); Python has no repository entity cache yet.

### Fixed

- `repository.Cache` inside a transaction (`interfaces.InTx`): `Get` and `Exists` read the
  store and never fill or read the cache (a fill could cache a row that never commits, a
  hit could predate the transaction's own writes, and the shared miss read could hand an
  uncommitted row to a reader outside the transaction); `Create` primes the cache only
  after the commit (`interfaces.RegisterAfterCommit`); `Update` and `Delete` evict at once
  and again after the commit, so a reader that raced the write cannot re-cache the
  pre-commit row for the TTL. Outside a transaction nothing changes.

## [0.0.17] - 2026-10-20

Python: unchanged (`techai-webutils` 0.6.1); the repository tooling substrate is Go-only for now
(ARCHITECTURE.md#python-parity-gap).
Breaking: `interfaces.OpService` and its mock are removed.

### Added

- A repository tooling substrate (ARCHITECTURE.md#repository-tooling-substrate):
  - `interfaces.Codebase` with Go, YAML and rendered-chart views over a memoizing
    `foundation/codebase` snapshot; `clients/codebase` kinds `git`, `fs` and `memory`;
    `foundation/yamlq` for YAML queries and in-place edits.
  - `interfaces.Probe[T]` / `ProbeSet[T]` and `services/probe.NewSet` (parallel, per-probe
    timeout, transient retry, strict mode); `types.Verdict` and `Verdict.Step`.
  - `types.ToolRun` and `services/toolrun` with target selectors and output parsers;
    `foundation/stepcache` (`off`, `dir`).
  - `foundation/report` (line, table, gh-actions, json, quiet, composite).
  - `interfaces.Generator`, `foundation/gen` (`Check`, `Apply`), `foundation/scaffold`,
    `foundation/impact`, `foundation/pins` (Renovate config from detected ecosystems and a
    consumer `Policy`), `foundation/schemagen` (JSON Schema, Kind extraction, config reference
    generator and audit) and `foundation/dataset` (merge-loaded YAML data, strict sections).
  - Command-line clients `clients/{git,helm,kubectl,docker,terraform}` (`stub|exec`), with
    `helm.NewRenderer`; `clients/process` (supervised child processes);
    `client.Output`, `client.SplitNUL` and `client.Call` / `CallOnce`.
  - AWS SDK-seam clients `clients/aws/{sts,secretsmanager,ses,bedrock,servicequotas,iam,route53}`
    (`stub|sdk`) and `clients/session` (verified, cached credentials).
  - `clients/toolchain`: tool manifests (`ToolEntry`, `ValidateToolEntries`), `Catalog`,
    `Detector`, `Installer` (per-platform installs, SHA-256-verified downloads, `CheckPins`) and
    `Registry`; `interfaces.Prompter`.
  - `execution/ops` (operations registry with exposure policies, `Func`, `WithDefaults`,
    `Decode`, `InputSchema`), `execution/runs` (background runs), and the front-ends
    `transport/cobra`, `transport/mcp` and `transport/cron`; `interfaces.Operation` /
    `Reservation` and `types.OperationDescriptor` / `ExposurePolicy` / `MCPSettings`.
- Dependencies: `github.com/invopop/jsonschema`, `github.com/modelcontextprotocol/go-sdk` and
  the AWS SDK `sesv2`, `bedrockruntime`, `servicequotas`, `iam` and `route53` modules;
  `github.com/pmezard/go-difflib` and `spf13/cobra` (v1.10.2) become direct.

### Removed

- `interfaces.OpService[A, R]` and `mocks.MockOpService`: an operation is registered in
  `execution/ops` instead. A consumer that still implements the contract declares it locally.

## [0.0.16] - 2026-10-19

Python: unchanged (`techai-webutils` 0.6.1); the entity bases come to Python in a later release.
Breaking: the role compositions are removed in favor of entity bases.

### Added

- An `Entity` base at every entity tier, typed by the entity and embedding the tier's `Base`:
  `store.Entity[C, T, P, ID]` (`store.NewEntity`, from `store.Funcs`), `repository.Entity`,
  `service.Entity`, `pipeline.Entity` and `workflow.Entity` (each `[T, P, ID, N]`, built over the
  next tier `N`, which `Next()` returns). Each serves exactly the roles its contract names
  (`Get`, `List`, `Create`, `Update`, `Delete`, `Exists`, `Insert`, `Upsert`, `PurgeBefore`) and
  fails construction when one has no statement or next-tier method. A component embeds one base
  and adds only its product operations.
- `interfaces.Upserter[T]` and `interfaces.Purger` roles; `foundation/decorate.Entity`, `Roles`,
  `RolesOf`, `RoleKind` and `RoleRunner`, the core the entity bases share.
- `interfaces.OutboxWriter`: `Enqueue(ctx, types.OutboxRecord)`, the producer side of the outbox,
  now part of `OutboxStore`. The postgres kind writes the pending row on the bound `*sql.Tx` (an
  id already enqueued is left untouched); the stub discards it.
- `repos/outbox.New(decs, name, store)`, the outbox repository.

### Removed

- The role compositions: `CRUD`, `CRUDNoList`, `ReadListExist` and `Resource` on the store,
  repository, service, pipeline and workflow bases; `store.Base.Outbox`, `FromFuncs`,
  `FuncResource` and the `b.Reader` … `b.Exister` builders; `repository.Base.Outbox`,
  `TxSafeInsert` and `Cached` (the cache is `repository.NewEntity`'s `cache` argument).

## [0.0.15] - 2026-10-18

Python: `techai-webutils` 0.6.1.

### Added

- `pipeline.Base.CRUD(svc)` and `workflow.Base.CRUD(pipes)` (Python `PipelineBase.crud`,
  `WorkflowBase.crud`): the CRUD role composition at the pipeline and workflow tiers, the same
  shape as `service.Base.CRUD`, so a CRUD resource's contract embeds `interfaces.Service` from the
  service tier up. The workflow runs Get and List as reads and Create, Update and Delete each as
  one command.

## [0.0.14] - 2026-10-18

Python: `techai-webutils` 0.6.0. Breaking: every tier base is now built from a contract, and the
operation-value API of 0.0.12–0.0.13 is removed.

### Changed

- Every tier base is built from its contract: `<tier>.NewBase[C](decs, name, …) (Base, error)`
  (Python `XBase(contract, decorations, name)`). Each method of `C` is one operation, decorated
  as `<name>.<op>` where `op` is the method name in snake_case (`decorate.OpName`: `FindByKey` →
  `find_by_key`). Running an op the contract does not declare fails `INTERNAL`. A non-interface
  contract is an error.
- Every tier package has the same layout: `<tier>.go` (`Tier()`, `Base`, `NewBase`) and
  `roles.go` (role compositions as `Base` methods). The same role names recur across tiers:
  `CRUD`, `CRUDNoList`, `ReadListExist` and `Resource` on store, repository and service;
  `Outbox` on store and repository (one `decorate.NewOutbox`); `Resource(next)` on pipeline and
  workflow. `repository.Cached` and `store.Resource`'s funcs moved to `(b Base) Cached` and
  `(b Base) FromFuncs`.
- `decorate.Subject` has the same views at every tier: `Once()` (no retry), `Observed()`,
  `Streaming()` (no retry, no timeout), `Component()` (any op on the component's chain) and
  `Timeout(op)`, plus `Exec` / `ExecOnce`. An op shares its component's breaker, bulkhead and
  rate limiter unless its own override enables one.
- `decorate.NewSubject(tier, name, ops, adjust...)` and `decorate.SubjectOf[C]`; nil ops gives a
  wire subject for op names that come from the wire. `client.NewWireBase(decs, name, opts...)`
  with `client.Producer()`, `client.Handler()` and `client.AtMostOnce()` replaces
  `NewProducerBase` / `NewHandlerBase`; `client.Kinds.Decorate` takes `(base, decs, name,
  opts...)`.
- A job has one base: `job.NewBase(decs, name)` with op `execute`; a gate's op is `run`.
- `decorate.Fixed(deps)` builds `Decorations` with fixed deps for tests;
  `decorate.Decorations.Inventory()` (Python `inventory()`) lists every resolved component and
  op with its layers and source config keys.
- Op labels in spans, metrics and logs are now snake_case contract method names, for example
  storage `presign_url`, `create_multipart_upload` and `list_objects_page_token`; outbox
  `get_queue_url`, `send_message_batch` and `put_object`; outbox store `claim`, `stats` and
  `delete_sent_before`.
- A zero `Base` is undecorated at every tier (no recovery). Tests build bases with
  `fixtures.Must(x.NewBase[C](fixtures.Decorations(), "test"))`.

### Removed

- Go: `interfaces.Op` and its mock; `store.Query` / `Statement`, `repository.Query` /
  `Command`, `service.Op`, `workflow.Read` / `Command` / `Saga`, `client.Call` / `CallOnce`;
  `decorate.Scope`, `Operation`, `OpFunc`; `store.Decorate`, `outbox.Decorate`,
  `repository.DecoratedFromConfig`, `repository.Settings` / `Deps`; the `NewBaseFrom`
  constructors, `client.Once`, `pipeline.Named` / `job.Named` and `pipeline.Subject(name)`;
  `tools/decorgen`, `tools/opmockgen` and `tools/internal`. `interfaces.OpService` stays.
- Python: `core.interfaces.op`, `foundation.decorate.scope`, the client `decorate` /
  `decorate_from_decorations` proxy, every `from_decorations`, `named`, the
  `query` / `command` / `read` / `saga` / `op` builders, `op_service` / `OpServiceImpl`, and
  `ClientBase.producer` / `handler` / `once`. `core.interfaces.op_service` stays.

## [0.0.13] - 2026-10-18

Python: unchanged (`techai-webutils` 0.5.5).

### Added

- `tools/opmockgen` and its `cmd/opmockgen` command: generates the mocks of operation contracts
  (structs of `interfaces.Op` fields, role interfaces and embedded structs of them). For each
  named struct it writes a `Mock<Name>` with one gomock mock per field, flattened through the
  embedded structs, a `NewMock<Name>(ctrl)`, and a `Contract()` that assembles the struct from
  them. A field whose interface is declared in `core/interfaces` uses the engine's published mock;
  any other uses the mockgen mock of the same name in the output package. Run it from a
  `go:generate` directive with `-source <contracts package> -out <file> <Struct>...`.

### Changed

- `tools/decorgen` now shares its import and type rendering with opmockgen through
  `tools/internal/gofile`; its output is unchanged.

## [0.0.12] - 2026-10-17

Python: `techai-webutils` 0.5.5.

### Added

- `interfaces.Op[In, Out]` (Python `core.interfaces.op.Op`): one named operation of a component,
  with one method, `Execute(ctx, in)`. A component whose verbs are not CRUD exposes them as `Op`
  values beside its role interfaces; `Pipeline` and `Workflow` have the same method set.
- Operation builders at every tier, each decorating its operation under its own name
  `<component>.<op>`, so the config tunes one query, command or call on its own:
  - store: `store.Query(base, op, fn)` runs on the client a statement on its context runs on;
    `store.Statement(base, op, fn)` does too, but only inside a transaction (outside one it
    fails INTERNAL without running).
  - repository: `repository.Query` (retried) and `repository.Command` (never retried); Python
    `RepositoryBase.query` / `command`.
  - service: `service.Op`, which validates an input that implements `interfaces.Validatable`
    before running; Python `ServiceBase.op` (an input with `validate()`).
  - workflow: `workflow.Read` and `workflow.Saga` (no transaction) and `workflow.Command` (one
    transaction); Python `WorkflowBase.read` / `saga` / `command`.
  - client: `client.Call(base, op, sdkFunc)` adapts an SDK function shaped
    `func(ctx, in, ...opt) (out, error)` (an AWS SDK v2 operation) into an `Op` under the client
    tier, and `client.CallOnce` never retries; Python `ClientBase.call` / `call_once`.
- `NewBaseFrom(decs, name, …)` for the store, repository, service, workflow and client bases
  (Python `from_decorations` already existed): the base resolves its own decoration and each of
  its operations by name.
- `decorate.Scope` (Python `Scope`), `Decorations.Scope(tier, name)`, `decorate.OpFunc` and
  `decorate.Operation(subject, fn)`: the per-operation resolution the builders share.
- `Decorations.Components()` (Python `components()`), the inventory of every component resolved,
  for a generator to write one decoration entry per component; and
  `Decorations.VerifyExplicit(prefixes...)` (Python `verify_explicit`), which is `Verify` and also
  fails `INVALID_INPUT` on a resolved component with no override of its own name.
- `mocks.MockOp[In, Out]`.

## [0.0.11] - 2026-10-17

Python: `techai-webutils` 0.5.4.

### Changed

- Breaking: an override that turns on a layer its tier does not run (retry on a workflow, a
  breaker on a store) is `INVALID_INPUT` from `Decorations.Deps`, instead of being ignored.
- Breaking (Python): `PipelineBase.from_decorations(name, …)` and `JobBase.from_decorations(name,
  …)` take `name` as a prefix: each stage or job resolves `<name>.<stage>` (an override of `name`
  still covers them all) and is labelled by that full name.
- Breaking: `interfaces.Updater[T, ID, U]` takes the update input type `U` (`Update(ctx, id, U)`);
  the old `Updater[T, ID]` is `Updater[T, ID, *T]`. `Writer[T, ID]` is now the composition of
  `Creator[T, *T]` and `Updater[T, ID, *T]`, with the same method set.
- `decorate.Writer` is the composition of the new `decorate.Creator` and `decorate.Updater`; its
  behavior is unchanged.

### Added

- Role interfaces `Creator[T, C]` (`Create(ctx, C)`, an input-generic create) and `Suggester[P]`
  (`SuggestValues`), and the composition `Resource[T, P, ID, C, U]`: `Get`, `List`, `Create`
  from `C`, `Update` from `U`, `Delete` and `Exists`.
- A Base implementation of the roles and of `Resource` at every tier, so a resource is assembled
  rather than hand-written:
  - store: `store.Reader`, `Lister`, `Creator`, `Updater`, `Deleter`, `Exister` and
    `store.Resource`, built by `store.NewResource(base, store.Funcs{…})` from one statement per
    role, each run on the client its context's statement runs on; writes run through `Write`, so
    only inside a transaction. A missing statement fails INTERNAL at construction.
  - repository: `repository.Base.Resource(store)`, every role decorated and `Create` never
    retried.
  - service: `service.Base.Resource(repo, service.Validators{Create, Update})`, rejecting an
    invalid write input before the repository.
  - pipeline: `pipeline.Base.Resource(name, svc)`, and `pipeline.Unit`, which adapts an
    error-only step into a stage function.
  - workflow: `workflow.NewResource(flows, pipes)`, whose reads run with no transaction and whose
    writes each run as one command.
- Decorators `decorate.Creator`, `decorate.Updater`, `decorate.Suggester` and `decorate.Resource`
  (`decorate.NewResource(ops, inner, validateCreate, validateUpdate)`).
- Per-component decoration, Go and Python:
  - overrides cascade along a component's dotted name: `api` covers `api.documents.create`, and
    the deepest override wins (`decoration.Tier.Overriding` / `Tier.overriding`);
  - a `gates` tier (`decoration.gates`): `gate.Tier()` and `Gate.Decorate(decs)` in Go,
    `tier()` and `GateBuilder.decorate(decorations)` in Python, run a gate as one decorated
    `Run` operation;
  - `pipeline.NewBaseFrom(decs, prefix)` and `job.NewBaseFrom(decs, prefix)` resolve each stage
    and job by its own name `<prefix>.<name>`, and `Base.Named(sub)` (Python `named`) scopes a
    set's stages or jobs beneath `sub`;
  - `Decorations.Subject(tier, name)` and `Decorations.Verify(prefixes...)` (Python `subject`,
    `resolve`, `verify`): a resolution error is recorded and fails `Verify`, as does an override
    under the prefixes that no component resolved.

## [0.0.10] - 2026-10-17

Python: `techai-webutils` 0.5.3.

### Changed

- Breaking: `workflow.Base` is an embeddable base like every other tier's. A workflow embeds it
  and runs each operation as a read (`Exec`, no transaction), a command (`ExecTx`, one
  transaction) or a saga (`Exec` whose body commits each local step with `InTx`, undecorated).
  `NewBase(name, tx, deps)` takes the workflow's name. `Func`, `Wrap`, `Read` and `Tx` are
  removed. Python: `WorkflowBase(name, tx, deps)` with `exec`, `exec_tx` and `in_tx`; `func`,
  `wrap`, `read` and `tx` are removed.
- Breaking: a workflow's series and spans are labelled by the workflow's name and the operation
  (`{workflow=<name>, operation=<op>}`, span `<name>.<op>`), in place of
  `{workflow=<op>, operation=Execute}`.
- `ExecTx` and `InTx` (Python `exec_tx`, `in_tx`) fail INTERNAL on a context already in a
  transaction, or with no transaction manager, without running their body.
- Breaking: the outbox relay moves from `services/outbox` to `workflows/outbox`, since a pass is a
  saga. `NewRelay(cfg, tx, store, sink, …)` takes the transaction manager its writes commit
  through: the claim commits in one transaction, delivery runs with none open, and each row's
  finalize commits in its own. An outcome is counted once its finalize commits.
- Breaking: the `postgres` outbox store's writes (`Claim`, `MarkSent`, `Retry`, `Park`,
  `DeleteSentBefore`) run on the `*sql.Tx` bound to the context and fail INTERNAL when none is;
  `Stats` reads on the bound transaction when there is one. `outboxtest.Harness` takes the `Tx`
  its writes run in.

### Added

- `store.Base.Write(ctx, op, fn)`: run a store write through the store tier only inside a
  transaction; outside one it fails INTERNAL without running `fn`.
- Python `core.interfaces.repository.in_transaction()` and `marking_transaction()`, the mirror of
  Go's `interfaces.InTx` and `WithInTx`.
- `workflow.Base.Pass(op, pass)`: run a background loop's pass as one decorated operation with no
  transaction; its method value is a `loop.Decorator`, in place of `Wrap`. Go only, as
  `workflows/loop` has no Python mirror.
- `stores/sqltx`: the `database/sql` transaction manager. It binds the open `*sql.Tx` to the
  context (`sqltx.Tx(ctx)`), runs after-commit hooks, and fails INTERNAL on a nested
  transaction.
- `tests/fixtures.Must(v, err)`: a test's wiring of a `(T, error)` constructor, panicking on the
  error.

### Fixed

- `stream.NewMetrics`: the depth gauges could stay one item off for as long as the producer was
  blocked, when the producer's and the consumer's updates raced; each change and its report now
  happen under one lock.

## [0.0.9] - 2026-10-17

### Added

- `core/interfaces.OutboxRepository` and `repository.Base.Outbox(store)`: the outbox store run
  through the repository tier. `Claim` runs without retry (it leases rows and counts an
  attempt); `MarkSent`, `Retry`, `Park`, `Stats` and `DeleteSentBefore` get the full chain.

### Changed

- `outbox.NewRelay` takes an `interfaces.OutboxRepository`. The method set is the
  `OutboxStore`'s, so existing callers compile; build the argument with
  `repository.Base.Outbox` to put the relay's store calls behind the repository tier.

## [0.0.8] - 2026-10-17

Python: `techai-webutils` 0.5.2.

### Added

- `decorate.LoadDecorations(loader, collaborators)` (Python `Decorations.load`): read the
  decoration section and build the `Decorations` in the one call a composition root makes.
- A generated `MetricsQuerier` mock.
- `tools/decorgen` (`go run …/go/tools/decorgen/cmd/decorgen -source <pkg> -out <file>
  <Interface>…`): generates a `Decorated<Name>` per interface whose context-first,
  error-last methods run through a `decorate.Subject` (named by the method) and whose other
  methods pass through — the Go counterpart of Python's runtime `decorate(client, base)`.

### Changed

- Breaking: `job.Base.Local(name, group, fn)` replaces `job.Base.Runnable` and
  `core/interfaces.Runnable` (and its mock): a runner-less job is a name, a group and a
  function, with no adapter type to implement.
- Breaking: a Connect client call is retried only when its schema declares the procedure
  idempotent (`idempotency_level` `NO_SIDE_EFFECTS` or `IDEMPOTENT`); a procedure of unknown
  idempotency runs at most once, since its first attempt may have landed.

### Removed

- Breaking: the config fields the decoration tiers replaced — `transport.rate_limit`
  (`transport.RateLimitConfig`; a controller's rate limit is `decoration.controllers`),
  `replay.op_timeout` (`infra.ReplayConfig.OpTimeout`; the replay client's timeout is
  `decoration.clients`), `pipelines.timeout` (`pipelines.Config.Timeout`) and the
  `schema/workflows` package (`workflows.timeout`); a pipeline's and a workflow's timeout
  are `decoration.pipelines` and `decoration.workflows`.

### Fixed

- `decoration.Load` keeps a dotted component name (`outbox.sqs`, `seed.ingress.gateway`): the
  layered YAML loader split such an override key into nested maps, so it decoded as an empty
  override of its first segment and was dropped. An override may be written as one dotted key
  or nested by segment.

## [0.0.7] - 2026-10-13

### Added

- Python parity with the Go repository, service, pipeline and workflow tiers:
  `foundation/decorate` (`OpMiddleware`, `chain`, `exec_op` and the recovery, tracing, metrics,
  logging, auth, timeout, retry and circuit-breaker middlewares); `repos.repo_op_chain` +
  `RepoOpDeps` and `services.service_op_chain` + `ServiceOpDeps`, which decorate custom repository
  queries and domain service operations with the tier's CRUD stack; `DelegatingRepository` and
  `DelegatingCrudService`, so a consumer type inherits decorated CRUD and adds only its custom
  ops; `ServiceBuilder.with_validation`, `with_metrics` and `with_tracing`; `pipelines.then`
  (Go `pipeline.Stage.Then`); and `workflows.tx_workflow` (Go `workflow.Base.Tx`).
- One shape for the Go request tiers (pipeline, workflow, service, repository), on one engine:
  `decorate.Tier` (a tier's name and layer order, as data), `decorate.Deps` and
  `decorate.Subject` (`Exec[R]`), plus role decorators (`decorate.Reader`, `Lister`, `Writer`,
  `Inserter`, `Deleter`, `Exister`) from which every decorated CRUD shape is composed. Each tier
  package is one file with a `Tier()` and a `Base` whose helpers are generic methods (Go 1.27):
  `pipeline.Base` (`Stage`, `FanOut`; `pipeline.Stage.Then`), `workflow.Base` (`Func`, `Wrap`,
  `Read`, `Tx`), `service.Base` (embeds `Exec`; `CRUD`, `CRUDNoList`, `OpService`) and
  `repository.Base` (embeds `Exec`; `CRUD`, `ReadListExist`, `CRUDNoList`, `TxSafeInsert`), with
  `repository.Cached` for the read-through entity cache.
- The store tier, `go/stores`, between the clients and the repositories. `stores/store`:
  `Tier()` (Tracing → Metrics → Logging → Timeout, decorated by `decoration.stores`),
  `Decorate(store, name, deps)`, and `Base[C]` (`NewBase(name, client, deps)`; `Exec` through
  the tier, `Client(ctx)` the transaction-bound client) with `WithClient` / `ClientFrom`; kinds
  `KindEnt` / `KindGORM`, selected by `stores.kind`. `repository.DecoratedFromConfig` puts its
  store behind the store tier.
- `core/interfaces.ReadListExistStore`, `CRUDNoListStore`, `CRUDNoListRepository` and
  `InsertOnlyStore`, with generated mocks for every role interface and `OpService`;
  `fixtures.Workflows(tx)` for tests that build workflows.
- Configurable decoration for every tier. `foundation/config/schema/decoration` (`Config`,
  `Load`, `DefaultConfig`, `Tier`, `Layers`, `Merge`) gives the clients, workflows, pipelines,
  services, repositories, stores and jobs tiers each `defaults` plus per-component `overrides`
  keyed by the component's name; every layer (recovery, tracing, metrics, logging, auth, timeout,
  retry, circuit breaker, bulkhead) is switched and tuned there, and a partial section changes
  only what it sets. `decorate.Decorations` (`NewDecorations(cfg, Collaborators)`, `Deps(tier,
  name)`) and `decorate.Build` turn a component's resolved layers into its `decorate.Deps`
  (`Deps.SkipRecovery` drops recovery). The default keeps today's behavior: clients, workflows
  and jobs observed, the pipeline, service, repository and store tiers not; retry and a breaker
  on repositories; a 30 s timeout, bulkhead and breaker on the `storage`, `outbox.sqs`,
  `outbox.s3` and `prometheus` clients.
- The execution substrate in the same shape: `job.Tier()` and `job.Base` (`Func`, `Runnable`,
  `Wrap`), which decorate every job once; `engine.Pool` (`FanOut` as a generic method);
  `engine.Step` / `engine.Steps` (`Serial`); and a chainable `*gate.Gate` (`Observe`, `Run`,
  `RunLocal`).
- The clients tier in the same shape. `clients/client` holds the client decoration tier
  (`client.Tier()`: Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging),
  `client.Base` (`Once`, `Do`, `DoOnce`, `Stream`), `client.NewHandlerBase` (observed, never
  retried — a message handler) and the kind registry `client.Kinds[C, D, T]` (`New`, `Build`,
  which takes the family's layers from `Decorations` under the client's `Config.Name`). Every
  family is a root `<family>.go` (`Kind` constants, a data-only `Config`, a `Deps`, a kinds table,
  `NewFromConfig(ctx, cfg, deps)`), a `decorated.go` with `Decorate(base, name, deps)` where the
  family is decorated, and one subpackage per backend. `foundation/decorate` gains the
  `LayerBulkhead` layer (`decorate.NewBulkhead`, `Deps.Bulkhead`); its timeout codes a deadline
  overrun `CodeTimeout`.
- `clients/cache/stub` and `transport/server/stub`, so every family with a meaningful stub boots
  with no infrastructure (`database` and `rpc` default to `postgres` and `grpc`).
- `core/interfaces.CQLSession` (`CQLQuery`, `CQLIter`, `CQLBatch`, `CQLBatchKind`): the CQL
  contract the analytics store runs on, with generated mocks.
- The controller tier: `transport.Tier()` (tracing as a server span → metrics → logging →
  recovery → rate limit → bulkhead → auth → timeout), switched and tuned per controller by
  the new `decoration.controllers` tier; `transport.Base` (`NewBase`, `Handler`) runs a REST or
  RPC handler through it. `transport.Interceptors` lays the tier out for an RPC framework, and
  `transport/connect/interceptors.ServerOptions` (with `Edge{Authn, Inner}`) and
  `transport/grpc/interceptors.ServerOptions` run every Connect and gRPC server RPC through it,
  their status-aware tracing, metrics and access logs standing in for the tier's own.
- `decorate.LayerRateLimit` (`NewRateLimit`, `Deps.RateLimiter`; `decoration.rate_limit`) and a
  shedding bulkhead (`Deps.ShedLoad`; `decoration.bulkhead.shed`, `bulkhead.Shed`): an operation
  over the rate, or arriving at a full bulkhead, fails `CodeResourceExhausted` at once.
  `decorate.Tier.Middleware` builds one layer, for protocol adapters.
- `clients/rpc/wire.MetadataCarrier`, the OTel carrier over gRPC metadata.
- Python (`techai-webutils` 0.5.1): every tier decorated like Go's, from the same `decoration`
  config section. `foundation/config/decoration` (`DecorationConfig`, `Tier`, `Layers` and the
  retry, circuit-breaker, bulkhead and rate-limit tunings; `default_config`, `from_mapping`,
  `load`) reads the YAML the Go services read, with Go durations parsed by
  `foundation/config/duration.parse_duration`. `foundation/decorate` (a package now) adds
  `Layer`, `Tier`, `Subject`, `Deps`, `Observability`, `Decorations`, `Collaborators` and the
  bulkhead (waiting or shedding) and rate-limit middlewares; `foundation/resilience/retrier`
  adds `ExponentialRetrier`. Each tier has Go's `tier()` and a base built
  `from_decorations(name, decorations)`: `clients.client.ClientBase` and `decorate` (a client
  whose every coroutine method runs through the client tier), `controllers.ControllerBase`,
  `repos.RepositoryBase`, `services.ServiceBase`, `pipelines.PipelineBase` (`stage`, `fan_out`;
  `Stage.then`), `workflows.WorkflowBase` (`func`, `wrap`, `read`, `tx`) and
  `execution.job.JobBase`. `core/interfaces/tracer.SpanKind` sets a span's kind.
- Python gRPC server and client on the controller and client tiers.
  `controllers/grpc/interceptors.server_interceptors(name, deps, Edge(authn, inner))` lays the
  controller tier out as `grpc.aio` interceptors (`controllers.interceptors.lay_out`, the
  `transport.Interceptors` twin) with gRPC-native `TracingServerInterceptor`,
  `MetricsServerInterceptor` (`grpc_server_requests_total`, `grpc_server_duration_seconds`) and
  `LoggingServerInterceptor`; `service_auth` and `ValidatingServerInterceptor` are its edge
  interceptors. `clients/rpc/grpc/interceptors.client_interceptors` runs each unary call through
  the client tier and injects the trace context and the service token;
  `new_rpc_channel_from_config(config, decorations)` installs it, named by the new
  `RpcConfig.name` and carrying `RpcConfig.service_token`.
- Python messaging on the client tier, as Go's: `clients.messaging.decorate` runs each publish
  through the client tier under the new `MessagingConfig.name` (op: the topic), and both
  backends run every handler call through the handler tier as `<name>.handler`. The publisher's
  trace context rides SQS message attributes and memory-broker metadata, and the handler
  continues it (`foundation/tracer.inject` / `continued`). `new_messaging_from_config` and
  `new_messaging_subscriber_from_config` take `decorations` and a shared memory `broker`.

### Changed

- Breaking: server transport moved out of `clients/` into `transport/`: `clients/transport` →
  `transport/server` (`NewFromConfig`, `Config`, `KindStub`, `KindConnect`) with
  `transport/server/stub`; the Connect server → `transport/connect`; the Connect server
  interceptors → `transport/connect/interceptors`; the gRPC server and its tracing, metrics and
  logging interceptors → `transport/grpc` and `transport/grpc/interceptors`. The Connect client
  options moved to `clients/rpc/connect/interceptors.ClientOptions`.
- Breaking: removed the hand-ordered server builders and the interceptors the controller tier
  replaces — Connect `NewServerBuilder`, `RecoveryInterceptor`, `RateLimitInterceptor`,
  `BulkheadInterceptor`; gRPC `NewServerBuilder`, `RecoveryServerInterceptor`,
  `RateLimitServerInterceptor`, `BulkheadServerInterceptor` — and `transport/decorators`
  (`HandlerBuilder`), replaced by `transport.Base`. A server's observability now wraps its
  recovery and load shedding, so a rejected or panicking RPC is still traced, counted and logged.
- Breaking: the JSON:API codec moved to `foundation/jsonapi` — the route table (`Role*`,
  `RouteEntry` with the new `Resource()`, `ResourceConfig`, `Config.MatchRoute`), the
  `Transform*` functions and `MemberCode`; `transport/jsonapi` keeps `Middleware` (now taking a
  `foundation/jsonapi.Config`) and `IsRESTRequest`. The generic converters moved from
  `transport/rpc` to `foundation/convert` (`EnumMapper`, `NewEnumMapper`, `ParseUUID`,
  `Int32OrPanic`, `CountToInt32`); `transport/rpc` keeps the Connect error mapping.
- Breaking: the request tiers' builders, decorator subpackages and pass-through bases are
  replaced by the tier `Base`s. Removed: `pipeline.BasePipeline` / `NewBasePipeline` /
  `pipeline.Then`; `workflow.BaseWorkflow` / `NewBaseWorkflow` / `NewTxWorkflow`; the
  `pipelines/pipeline/decorators`, `workflows/workflow/decorators`, `services/service/decorators`
  and `repos/repository/decorators` packages; `service.BaseService` / `service.New` /
  `NewCRUDNoListService` / `OpChain`; `repository.BaseRepository` / `repository.New` /
  `NewReadListExistRepository` / `NewCRUDNoListRepository` / `NewTxSafeInsertRepository` /
  `OpChain` / `NewOpChain` / `OpDeps`; the `services` package (`Base`, `Build`); `decorate.MetricsSpec`;
  `interfaces.DecoratedRepository` / `DecoratedService` (use `Repository` / `Service`).
  `decorate.NewLogging`, `NewTracing`, `NewMetrics` and `NewRecovery` take `(…, tier, name)`.
  `repository.DecoratedFromConfig` keeps its signature and returns `interfaces.Repository`.
- Breaking: every request tier emits `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds` labelled `{<tier>, operation}` (pipelines and workflows
  emitted `<tier>_executions_total` / `<tier>_execute_duration_seconds` by name only; the
  repository label was `repo`), and spans carry `<tier>.name` / `<tier>.operation`.
- Breaking: the execution substrate drops its builders and standalone runners. `job.Adapt` →
  `job.Base.Runnable`; `job/decorators` → `job.Base` (jobs emit `job_operations_total` /
  `job_errors_total` / `job_operation_duration_seconds` in place of `execution_job_runs_total` /
  `execution_job_duration_seconds`, and a job whose results fail is no longer a `failed` outcome);
  `engine.FanOut(ctx, items, n, fn)` → `engine.Pool(n).FanOut(ctx, items, fn)`;
  `engine.NewStep` / `StepWarnOnly` / `RunStepsSerial` → `engine.Step{…}` / `engine.Steps.Serial`;
  `gate.NewGate(…)…Build()` → `gate.New(…)…`, `WithObserver` → `Observe` (nil keeps the
  current observer), `gate.RunGate(ctx, r, root, g)` → `g.Run(ctx, r, root)` and
  `gate.RunGateNoRunner(ctx, g)` → `g.RunLocal(ctx)`. Removed as unused: `job.New` / `Builder` /
  `Job` / `JobConfig` / `Single` and its options, `job.When` / `Conditional`, `job.Tag` /
  `TaggedJob` / `FilterByLang` / `LangTag`, `engine.WorkUnit`, `engine.RunStepsParallel`,
  `engine.StepFunc` and `engine.WithObserverCtx` / `ObserverFromCtx`.
- Breaking: client kinds are strings (`client.Kind`) and every family builds through
  `NewFromConfig(ctx, cfg, deps)` with its runtime collaborators (observability, SDK clients,
  shared Redis client, …) in a `Deps`, never in `Config`. Removed: each family's `New(kind,
  opts...)`, functional options (`With*`), `Kind.String()`, `ParseKind` and `ToOptions`;
  `messaging.NewPublisher` / `NewSubscriber` / `NewAdapter` (use `messaging.NewFromConfig` /
  `NewConsumerFromConfig`); `storage.New`; `prometheus.New` / `HTTPDoer` / `DecorateDoer` /
  `Sample`, now in `clients/prometheus/http`. `messaging.Config` takes the env-bound
  `infra.SQSConfig` plus `Receive` (nack delay, backoff, long poll); `outbox`, `lock`,
  `replaybuffer`, `prometheus` and `analytics` take their client-tier tuning as `Client
  client.Config` (outbox's `Resilience` is renamed). `outbox/sqs.New` and `outbox/s3.New` take
  the SDK client as an argument instead of `Config.API`. `connector.NewSourceBuilder(cfg, deps)`.
  `sqs.NewPublisher` and `sqs.NewSubscriber` take a `ctx` (`NewPublisherContext` is removed).
- Breaking: `clients/cassandra` is renamed `clients/cql`, with string kinds `cassandra` (default)
  and `keyspaces`; each kind subpackage dials through the gocql adapter in `clients/cql/driver`
  (`cassandra.Cluster` / `keyspaces.Cluster` build the configuration without dialing).
  `cql.Session` and friends move to `core/interfaces.CQLSession` etc. `analytics.Config`'s
  `Cassandra` field is `CQL`, and `analytics.Deps.Dial` replaces `SessionFactory`.
- Breaking: one decoration engine for the clients tier. Removed: `clients/decorators` (`Stack`),
  `foundation/decorator`, and the `analytics`, `cache`, `connector`, `jobs`, `lock`,
  `messaging`, `outbox`, `replaybuffer` and `storage` `decorators` packages. Decorated clients
  emit `client_operations_total` / `client_errors_total` / `client_operation_duration_seconds`
  labelled `{client, operation}`, replacing `lock_operation*`, `replaybuffer_operation*`,
  `messaging_handle_*` (a subscriber's handler is the client `messaging.handler`) and the
  analytics store's `analytics_executions_total` / `analytics_execute_duration_seconds` /
  `analytics_errors_total`. `cache` is decorated fail-open (a tier failure reads as a miss) and
  keeps `cache_hits_total` / `cache_misses_total` labelled `{cache}`;
  `cache_operation_duration_seconds` is `client_operation_duration_seconds{client="cache"}`.
- Breaking: decoration comes from config, not hand-passed collaborators. Every client family's
  `Deps` embeds `decorate.Decorations` (and its `Config` gains `Name`, replacing `Client
  client.Config`); `client.Config` / `DefaultConfig` / `DepsFromConfig`, `decorate.Tiers` /
  `NewTiers` and `reswire.NewRetrier` / `NewBreaker` / `NewBulkhead` are removed.
  `repository.DecoratedFromConfig` takes `Deps{Decorations, Cache, IDOf}`, returns an error, and
  `Settings` keeps only `CacheTTL` / `CacheVersion`. `river.NewInsertClient` takes the
  `decorate.Decorations` and runs each insert as the `river.insert` client
  (`river.InsertClientName`), resolved from `decoration.clients`. The messaging SQS and Redis
  backends take a ready-built `Handlers client.Base` in place of `Metrics` / `Tracer` /
  `HandlerTimeout`.
- Breaking: RPC clients run on the client tier. `clients/interceptorcore` and the per-concern
  client interceptors are removed — gRPC `ClientBuilder`, `StreamingClientBuilder`,
  `Retry*`, `CircuitBreaker*`, `Timeout*`, `Metrics*Client*`, `Tracing*Client*`,
  `Logging*Client*` and `ServiceAuth*ClientInterceptor`; Connect `ClientBuilder`,
  `RetryInterceptor`, `CircuitBreakerInterceptor`, `TimeoutInterceptor` and
  `ClientTracingInterceptor`. In their place `interceptors.ClientOptions(base, serviceToken)`
  (gRPC) and `interceptors.ClientOptions(base)` (Connect) run each call through a
  `client.Base`, and `rpc.NewFromConfig` installs them (`rpc.Config` gains `Name` and
  `ServiceToken`; `rpc.Deps` embeds `decorate.Decorations`). RPC client series are
  `client_*{client, operation="pkg.Service/Method"}`, replacing `grpc_client_*` and
  `grpc_client_stream_*`; a retried call returns its last error rather than Unavailable
  "retries exhausted". A gRPC stream's open is never retried.
- `clients/rpc/wire` (`FromError`, `Code`, `ToGRPC`, `ToConnect`): the RPC-status ↔ domain-code
  codec; `transport/rpc.FromRPCError` delegates to it. `Unimplemented` now maps to
  `CodeInvalidInput` (was `CodeUnknown`), so it is never retried.
- `client.Base.Open`: open a long-lived stream with the client's timeout bounding only the open.
  The client tier's spans are `SpanKindClient` (`decorate.Tier.SpanKind`; `decorate.NewTracing`
  takes the kind). `bulkhead.Shed` runs a call only when a slot is free (the server bulkhead
  interceptors use it).
- Fixed: the gobreaker circuit breaker's rejection (open, or half-open over quota) is
  `CodeUnavailable` instead of an uncoded error. The exponential retrier's `MaxRetries` counts
  retries as documented — it made `MaxRetries` total attempts, one fewer than configured.
- Breaking: persistence moves to `go/stores`. `repos/repository/compiler/ent` and
  `repos/repository/entlist` → `stores/ent` (`compiler.Apply` → `ent.Apply`); the
  `repos/repository/compiler` Kind enum (with its never-implemented mongo / elastic kinds) is
  removed; `repos/adapters/gorm` → `stores/gorm` (`Kind` is the string `"postgres"`);
  `repos/errors` → `stores/errors` (`NewDBMapper(kind string)`, `KindPostgres = "postgres"`).
  Config: `repos.compiler.kind` → `stores.kind`; `repos.timeout` is removed (the decoration
  config's repositories tier owns it).
- Breaking: `core/errctx` is merged into `core/errors`: `errctx.WithSink` / `Capture` /
  `Captured` are `errors.WithSink` / `Capture` / `Captured`.
- Breaking: the outbox store moves to the store tier. `repos/outbox/pgstore` →
  `stores/outbox/postgres`; `stores/outbox.NewFromConfig(ctx, cfg, deps)` builds it by kind
  (`KindStub` default, `KindPostgres` over `Deps.DB`) behind the store tier, named `Config.Name`
  (default `outbox`). `interfaces.OutboxStore` gains `DeleteSentBefore`. The test-only
  `outboxtest.SQLStore` reference store is removed; the conformance suites run against the
  postgres kind.
- A message handler continues its publisher's trace: `tracer.Inject` / `tracer.Extract`
  carry the process propagator's context (traceparent, tracestate, baggage) on SQS message
  attributes and memory-broker metadata, and the SQS subscriber hands a message's string
  attributes to the handler as `Message.Metadata`. Redis Pub/Sub carries the payload alone, so
  a trace stops at a Redis channel.
- Removed the unused jobs client: `clients/jobs` (`NewFromConfig`, `Config`), `core/interfaces`
  `Job`, `Worker`, `PeriodicJob`, `JobEnqueuer`, `JobScheduler` and `WorkerRegistry`, and the
  River `Client` / `Config` / `EventPublisher` / periodic jobs / workers;
  `clients/jobs/river` keeps `InsertClient`, `Migrate` and `Runtime`. Also removed the dead
  `messaging/sqs.Adapter`.
- Breaking (Python, 0.5.1): the tier builders, op chains and proxies are replaced by the tier
  bases. Removed: `foundation.decorator`; `repos` `RepositoryBuilder`, `BaseRepository`,
  `repo_op_chain`, `RepoOpDeps`; `services` `ServiceBuilder`, `BaseCrudService`,
  `service_op_chain`, `ServiceOpDeps`, the op-service `build`; `pipelines` `PipelineBuilder`,
  `AsyncPipelineBuilder`, `BasePipeline`, `BaseAsyncPipeline`, `then`; `workflows`
  `WorkflowBuilder`, `AsyncWorkflowBuilder`, `BaseWorkflow`, `BaseAsyncWorkflow`, `tx_workflow`;
  `controllers.decorators.HandlerBuilder`; `execution.job.decorators`; the client proxies
  (`LoggingProxy`, `RetryProxy`, `CircuitBreakerProxy`, `MetricsProxy`, `HedgeProxy`,
  `RateLimitProxy`, …) with `new_client_stack_from_config` / `ClientStackConfig`, and the
  `event_stack` / `job_stack` helpers. The synchronous pipeline and workflow variants are gone.
  The tier series follow Go's names: `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds`, labelled `{<tier>, operation}` (the client tier drops the
  former `outcome` / `code` labels).
- Breaking (Python): the gRPC server moved under the controller tier. `clients.transport.grpc`
  (`GracefulServer`, `ServerConfig`, `create_server`), `clients.rpc.grpc.errors` and
  `clients.rpc.grpc.interceptors.auth` → `controllers.grpc`; `ServerInterceptorBuilder` and the
  client `InterceptorBuilder` with its retry, timeout, circuit-breaker, logging, metrics and
  tracing interceptors are removed — use `server_interceptors` and `client_interceptors`.
- `techai-webutils` versions: 0.5.0 carried the Python tier additions above (no Go change); 0.5.1
  carries the breaking Python changes above.
- Breaking (Go): `transport/grpc/interceptors.ServerOptions(name, deps, edge)` takes an `Edge`
  (`Authn` unary and `StreamAuthn` stream interceptors, run just before Auth) and now decorates
  stream RPCs too (no timeout on a stream); `Tracing`, `Metrics` and `Logging` gain
  `*StreamServerInterceptor` twins.
- The controller tier is the edge (`decorate.Tier.Edge`, Go and Python): a REST handler's failure
  is logged at Warn for a client fault and at Error otherwise, where inner tiers log at Debug.
- Messaging spans carry their role, Go and Python: a publish is a PRODUCER span
  (`client.NewProducerBase` / `ClientBase.producer`) and a handler call a CONSUMER span
  (`client.NewHandlerBase` / `ClientBase.handler`), where both were CLIENT spans.
- `client.Kinds.AtMostOnce` lists the kinds `Build` never retries; the Redis messaging
  publisher uses it in place of a special case in `messaging.NewFromConfig`.
- Python: a decorated client (`clients.client.decorate`) forwards `async with` to the client
  it wraps; a refused auth layer raises `ForbiddenError("<name>.<op>: not allowed")`.
- A controller's bulkhead sheds by default once enabled (`decoration.controllers` defaults
  `bulkhead.shed: true`; the bulkhead itself stays off), Go and Python.
- Recovery logs a panic with its stack even when the tier's logging layer is off
  (`decorate.Deps.PanicLogger` / Python `Deps.panic_logger`, set by `Decorations` whenever
  recovery is on).

### Fixed

- Python: a `null` decoration section (an empty YAML key) decodes as its default instead of
  `None`.
- A tier rejection on the Connect and gRPC servers (a denial, a rate limit, a panic) answers
  only its code's client-safe message (`clients/rpc/wire.SanitizeConnect` / `SanitizeGRPC`;
  Python `controllers.grpc.safe_status`); the real error is captured for the access log instead
  of reaching the caller.
- A gRPC server continues its caller's trace: `TracingServerInterceptor` extracts the W3C
  trace context the client injected into the incoming metadata instead of starting a new
  trace per hop.
- Python: the gRPC client's span was tagged `kind="client"` as an attribute instead of being a
  CLIENT span, and the client injected neither the trace context nor the service token; both
  now ride every call. A rate-limited Python gRPC call was answered UNIMPLEMENTED; the
  controller tier answers RESOURCE_EXHAUSTED.

## [0.0.6] - 2026-10-12

### Added

- `workflows/loop.Spec` + `NewGroupFromSpecs`: a service declares its background loops as
  data (run, name, op, schedule) and the composition root builds one decorated `Group`.
- `core/interfaces.KeyedLimiter` and its in-process backend `foundation/resilience/bulkhead/keyed`:
  a non-blocking per-key concurrency cap (one tenant cannot take every slot).
- `foundation/config.Section[T]`: unmarshal and validate one typed config section (value- or
  pointer-receiver `Validate`); `foundation/tracer.NewFromTracing`: a service's tracer from
  the tracing section, falling back to noop so a missing collector never blocks boot.
- `foundation/chartspec.Validate`: validate an already-decoded `types.ChartSpec` exactly as
  `Parse` validates JSON, so a proto-mapped spec needs no JSON round trip. A shelf node with
  both a field and an operator is now `CodeInvalidInput` on both paths.
- `repos/outbox/pgstore`: the production PostgreSQL `OutboxStore` over a product's own outbox
  table, named by config (table, key, tenant and attribute columns, lease, error cap), plus
  `DeleteSentBefore` for retention; proven against the `outboxtest` conformance suite.
- `repos/repository.OpDeps` + `NewOpChain`: a repository's custom-op chain built from one
  bundled value instead of six positional collaborators.
- `foundation/resilience/pausegate`: a receive gate that closes for a configured pause after an
  outage-coded error (default `CodeUnavailable`), with an optional paused gauge, so a consumer
  stops pulling messages it cannot process during a downstream outage.

### Changed

- Python `new_client_stack_from_config` is generic: it returns the stack typed as the client it
  was given, so callers no longer `cast` the result.
- `techai-webutils` is published as 0.4.0. PyPI already holds 0.1.0 through 0.3.3 from the
  package's earlier release line, which also made those the newest by version; continuing above
  them means no collision and an unpinned install gets this line. The release PR now sets the
  Python version by hand (AutoGitSemVer is removed: the repo's Go `v*` tags reset its count).
- Releases are cut by merging to `main` (`scripts/release.sh`): the Go module is tagged `vX.Y.Z`
  from the newest `CHANGELOG.md` section, and the Python package is published and tagged
  `techai-webutils-vX.Y.Z` from its pyproject version. A tag push no longer triggers a release,
  and CI's `release-check` fails a PR whose versions would re-release or go backwards.
- `core/interfaces.AnalyticsStore` embeds `HealthChecker` (breaking for out-of-tree
  implementations): the Cassandra store's `Readiness` runs `SELECT release_version FROM
  system.local` and is `CodeUnavailable` when stopped or unreachable; the stub is always ready;
  the decorated store delegates the probes undecorated.

## [0.0.5] - 2026-10-11

### Fixed

- `clients/cassandra` (self-managed kind): a cluster with `DisableHostDiscovery` selects hosts
  DC-aware round robin instead of token-aware. With one pinned endpoint token awareness routes
  nothing, and gocql v1.7.0's token-aware policy dereferenced a nil host once that endpoint
  left its ring during an outage, crashing the process from a driver goroutine. Discovering
  clusters keep the token-aware policy.

## [0.0.4] - 2026-10-11

### Added

- `clients/messaging/sqs.NewAWSClient`: builds the SDK SQS client the publisher and subscriber
  use (region, endpoint override, static or default-chain credentials) for a caller that
  sends through the SDK directly. An access key without its secret is `CodeInvalidInput`.
  No I/O until the first call.
- `go/tests/mocks`: generated mocks for `CacheInvalidator`, `Encryptor`, `EventPublisher`,
  `JobEnqueuer`, `JobScheduler`, `WorkerRegistry`, `Locker`, `CountingPublisher`,
  `MessageConsumer`, `Repository`, `Source` and `Transaction`, so a consumer imports the
  engine's mocks instead of regenerating them.
- `testdata/analytics_fact.golden.json`: a dim holding `<`, `>`, `&` and U+2028 pins that Go
  and Python escape them identically.
- `pipelines/pipeline.Then`: composes two pipelines into one, the first's output feeding the
  second. An error from the first is returned unchanged and the second does not run; each
  stage keeps its own decorators. A nil stage panics.
- `workflows/loop`: background work as decorated workflow passes. `Pass` adapts a
  `func(context.Context) error` into a `Workflow[struct{}, struct{}]`. `NewTicker` runs a pass
  per `Config.Interval` (back to back when zero; `RunAtStart` adds a startup pass), logs a
  failed pass and waits `ErrorBackoff`, and returns nil when its context ends. A negative
  duration is `CodeInvalidInput`. `Group` is an `interfaces.Lifecycle` that runs `Runner`s
  (`RunnerFunc` adapts a function) together for the lifetime of its `Start` context and logs
  one that stops with an error without stopping the others; a runner or pass that fails
  after the shutdown began is logged at debug, or not at all when it returns the context's
  own error; `Stop` cancels and waits within its context (`CodeTimeout` /
  `CodeCanceled`), and a second `Start` is `CodeConflict`.

## [0.0.3] - 2026-10-11

### Added

- `core/interfaces.Stream[T]`: a pull-based, closable sequence that every large result can
  travel on. `Close` may come from another goroutine and ends a running `Next`.
- `foundation/stream`: `FromSlice`, `Chunk` (greedy split at a byte budget; an oversized item
  is `CodeResourceExhausted`) and `Collect` (drain under a byte budget, closing the source
  on every path). `Buffer` decouples producer from consumer, selected by `Kind` + `Config` +
  `NewFromConfig` (`ParseKind`: `direct`, the default, or `bounded`, which reads ahead on a
  goroutine bounded by item count and bytes and blocks the producer when full). A context
  deadline ending a read is `CodeTimeout`; a cancellation is `CodeCanceled`. `NewMetrics`
  decorates a `Buffer` with the `stream_buffer_items` and `stream_buffer_bytes` gauges and
  the `stream_buffer_producer_wait_seconds` histogram, labelled `buffer`.

## [0.0.2] - 2026-10-11

### Changed (breaking)

- `foundation/listquery`: `Set`, `NewSet` and `ParseList` are removed. `ParseSort(m, spec)`
  sorts against the `Map`: only fields declared with `Field.Sortable()` may be sorted, and an
  unknown, unsortable or repeated field, an empty token or a bad direction is
  `CodeInvalidInput` instead of being dropped.
- `foundation/chartspec`: `CubeSchema` is replaced by `listquery.Schema`; `Compile` and `Drill`
  take it.
- `core/types.ChartSpec`: `FilterJSON json.RawMessage` is replaced by `FilterTree *FilterNode`
  (the wire key stays `filter`).

### Added

- `core/types.FilterNode`: the filter tree with a JSON codec for the listquery grammar, and
  `MaxFilterDepth`. `FilterOperator.Known` reports whether an operator is one of the defined
  set.
- `foundation/listquery.ParseFilter`: checks a `FilterNode` against a `Map`; `Parse` is now
  built on it. Integers outside ±(2^53−1) are rejected, and so is an empty or unknown
  operator in a tree built in code (`CodeInvalidInput`).
- `testdata/listquery/filters.golden.json`: the shared filter corpus.
- `foundation/chartspec`: a content-addressed node encoding for `ChartSpec`. `Node` and `Hash`
  (SHA-256 over `"chartspec/v1\x00"` and the node's RFC 8785 canonical JSON); `Encode` splits a
  spec into a `Tree` of shared nodes, `Decode` rebuilds it through a `NodeGetter` and verifies
  each node against its hash, and `Diff` reports the changed paths between two roots as
  `Change`s (a zero side is one root `Change`). `Encode` rejects an unknown filter operator,
  stores a nil `[]any` operand as an empty array, and treats a typed-nil field as absent. New
  dependency: `github.com/gowebpki/jcs`.
- `testdata/chartspec/nodes.golden.json` and `testdata/chartspec/specs.golden.json`: pinned
  node and root hashes.
- `clients/cassandra`: `CassandraConfig.DisableHostDiscovery` pins a cluster reached through
  a proxy, tunnel or port mapping to its one IP-literal contact point, translating every
  address the cluster advertises back to it; `ReconnectInterval` sets how often downed
  hosts are retried. `WithDisableHostDiscovery` and `WithReconnectInterval` set them.

## [0.0.1] - 2026-10-10

### Added

- Initial release of the Go module `github.com/gt-tech-ai/knowledge-engine-toolkit` and the
  Python package `techai-webutils`: the layered substrate (core → foundation → clients →
  repos → services → pipelines → workflows → transport), its `Kind` + `Config` factories,
  decorator stacks, the execution fan-out engine, and the test fixtures and generated mocks.
  `ARCHITECTURE.md` describes each layer.

[Unreleased]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.14...HEAD
[0.0.14]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.13...v0.0.14
[0.0.13]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.12...v0.0.13
[0.0.12]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.11...v0.0.12
[0.0.11]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.10...v0.0.11
[0.0.10]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.9...v0.0.10
[0.0.9]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.8...v0.0.9
[0.0.8]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.7...v0.0.8
[0.0.7]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.6...v0.0.7
[0.0.6]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/releases/tag/v0.0.1
