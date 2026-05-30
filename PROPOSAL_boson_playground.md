# Proposal: Boson Playground

## Summary

Build a browser-based Boson playground, similar in spirit to
`go.dev/play`, that lets a user edit a small Boson program, compile it
through the real toolchain, run it in a constrained environment, and inspect
the generated `.bs` assembly and `.bo` object metadata when needed.

The first version should be intentionally narrow: single-package programs,
Linux/amd64 execution, deterministic resource limits, and a small curated set
of examples. The playground should reuse `bosc`, `bas`, and `bld` rather than
implementing a second compiler path.

## Motivation

Boson is still small enough that most learning and debugging sessions start
with a short program. Today that means cloning the repository, installing the
external `mmk` build tool, building `bosc`, `bas`, and `bld`, and learning the
`.bos -> .bs -> .bo -> executable` pipeline before writing the first line of
Boson.

A playground lowers that barrier. It also gives compiler developers a shared
reproduction format: a short source snippet plus compiler, assembler, object,
linker, and stdout/stderr output captured from the same backend the local test
suites use.

Two distinct audiences:

- **Learners** — want a single editable buffer, immediate run feedback, and a
  forward path through curated examples (eventually the tour, see
  [PROPOSAL_boson_tour.md](PROPOSAL_boson_tour.md)).
- **Compiler contributors** — want a stable repro format with toolchain
  commit, full per-stage output, and structured diagnostics they can paste
  into an issue.

The playground should serve both without splitting into two products.

## Goals

- Edit and run a Boson `main` package in the browser.
- Show compiler diagnostics with source locations and editor markers.
- Surface the inspectable pipeline that makes this repo useful: source,
  generated `.bs`, optional `bdump` output, linker result, and program output.
- Provide shareable examples by content hash or saved snippet ID.
- Execute untrusted code with time, memory, process, and filesystem limits.
- Keep playground semantics tied to the checked-in toolchain, not to a
  browser-only reimplementation.
- Make runs reproducible: identical (source, stdin, toolchain commit)
  produces identical artifacts.
- Keep cold-path run latency under one second for typical programs using the
  prebuilt runtime bundle.

## Non-goals

- Full multi-package project hosting in the first version.
- Interactive terminal support (stdin is a single buffer, not a tty).
- Network access from executed Boson programs.
- Long-running benchmarks.
- A permanent source archive with user accounts.
- Cross-architecture execution before the toolchain has real target
  abstraction (see [PROPOSAL_arm64_backend.md](PROPOSAL_arm64_backend.md)).
- In-browser compilation in v1; the toolchain runs server-side. WebAssembly
  is a future direction, not a v1 requirement.
- Formatter integration. There is no Boson formatter yet.

## Current-state fit

The current pipeline is already well suited to a playground because each stage
emits an inspectable artifact:

```text
.bos -> bosc -> .bs -> bas -> .bo -> bld -> ELF64 executable
```

The repository has:

- `cmd/bosc`: source compiler and import discovery via `-listimports`.
- `cmd/bas`: assembler from textual `.bs` to `.bo`.
- `cmd/bld`: linker wrapper over `gbasm.LinkExe`.
- `cmd/bdump`: object-file inspector.
- `runtime/`: built-in packages that normal programs import.
- `boson.mmk`: useful build logic, but too shell/build-system-specific to be
  the playground API.

The playground should wrap the binaries directly instead of invoking `mmk`.
That keeps request handling explicit and makes sandboxing easier. Runtime
packages are precompiled once into a playground runtime bundle; each request
only compiles the submitted `main.bos`, assembles the resulting `main.bs`,
and links `main.bo` with the prebuilt runtime objects.

## User experience

The initial page should be the working editor, not a marketing page.

### Layout

Desktop (>= 1024px wide):

- Left pane: source editor for `main.bos`, full height.
- Right pane (resizable): output on top, artifact tabs underneath.
- Top bar: Run, Share, Reset, Examples dropdown, toolchain version chip.

Mobile / narrow (< 1024px):

- Flat tab strip across the top: Source / Output / Assembly / Object /
  Imports.
- Single full-width pane below.
- Floating Run button at the bottom-right.

### Editor

- CodeMirror 6 — smaller than Monaco, modular, works well on mobile.
- Line numbers, monospace, tab-as-tab (Boson source uses tabs in this repo).
- Boson syntax highlighting via a Lezer grammar derived from
  `cmd/bosc/lexer.go`. v1 can ship with a basic regex-based highlighter and
  upgrade to a proper grammar later.
- Inline error markers from parsed compiler diagnostics; squiggle at the
  reported line/col with the diagnostic message on hover.
- Editor URL shortcut: `?ex=hello` loads an example; `?id=<sha>` loads a
  saved snippet.

### Output pane

- Default view: combined program stdout + stderr, plus a status header line
  (`compiled in 12ms, ran in 4ms, exited 0`).
- Toggle: structured step view — one row per pipeline stage (bosc, bas, bld,
  run) with argv, exit code, duration, stdout, stderr.
- Truncation indicator if output exceeded the cap.
- Diagnostics surface as both editor markers and a list at the top of the
  output pane, each clickable to jump to the source position.

### Artifact tabs

- **Assembly** — the `.bs` produced by `bosc`, syntax-highlighted as plain
  assembly (no Boson highlighting needed; `.bs` is its own dialect).
- **Object** — a structured view of `.bo` contents, conceptually the same
  information `bdump` prints, presented as collapsible sections: functions,
  vars, data, struct shapes, relocations.
- **Imports** — the imports declared by the submitted source as a list
  (eventually a graph).
- **Link** — the linker stdout/stderr and the final symbol layout summary.

### Actions and keyboard shortcuts

- Run: Ctrl/Cmd+Enter.
- Share: Ctrl/Cmd+S.
- Reset to example: Ctrl/Cmd+Shift+R (only meaningful when an example is
  loaded).
- Focus editor: Esc.
- Navigate tabs: Ctrl/Cmd+1..5.

A `?` overlay should list these. Keep the rest of the UI free of in-app
instructional clutter.

### URLs and history

- `/play` — empty editor with default example.
- `/play?ex=<id>` — load a named example.
- `/play/<snippet-id>` — load a saved snippet by content-addressed id.
- Run does not change the URL. Share is the explicit save action.

## Frontend architecture

- A single static SPA served by `bplayd` from an embedded asset bundle
  (Go's `embed`).
- **Build prerequisites added by this proposal: npm (node).** That is the
  only new tool the repo gains. Everything else — the editor library, the
  bundler, the TypeScript compiler — is an npm dependency pulled in
  automatically. The contributor prerequisites become go + make + mmk +
  node/npm.
- Bundler: `esbuild`, installed as an npm dependency. Single command
  (`npm run build`) produces a minified bundle in milliseconds with no
  configuration files multiplying. No vite, no webpack — esbuild alone
  covers both dev rebuild and production output.
- Editor: CodeMirror 6, also an npm dependency. Modular enough to keep the
  bundle small; works on mobile.
- Language: TypeScript or plain JS — esbuild handles both transparently.
  TypeScript is preferred for the structured response shapes (run results,
  diagnostics) but is not load-bearing.
- No framework in v1. The surface area (one editor, one output, four tabs)
  does not justify the weight; vanilla TS + a tiny render layer is enough.
- Bundle target: under 250 KiB gzipped including the editor.
- Theme: light and dark, system-preference default, persistent override.
- Accessibility: keyboard-only navigation, ARIA labels on tabs and buttons,
  contrast-checked tokens, focus rings preserved.
- No analytics or third-party scripts in v1.
- Build integration: `make playground` (or the equivalent mmk rule) runs
  `npm install` and `npm run build` before invoking `go build`. CI does
  the same. The npm step is skippable on a checkout that has the prebuilt
  bundle committed — but the default expectation is that contributors
  rebuild, just like they do for the toolchain itself.

## Execution model

### Playground runtime bundle

The Docker image (or local build target) creates a fixed runtime bundle before
`bplayd` serves requests:

```text
/usr/local/lib/boson/playground/
  importcfg
  objects/
    builtin.bo
    _init.bo
    _heap.bo
    _io_sys.bo
    string.bo
    io.bo
```

The bundle contains:

- **All precompiled runtime objects** needed to link playground programs.
  These are trusted toolchain artifacts produced with the image.
- **A static `importcfg`** containing only packages user code may import.
  This file is the user-import policy and package index. If a package is not
  present in `importcfg`, submitted source cannot import it.

For the current runtime shape, the runtime object bundle includes
`builtin.bo`, `_init.bo`, `_heap.bo`, `_io_sys.bo`, `string.bo`, and `io.bo`.
The v1 user-importable `importcfg` contains:

```text
builtin=/usr/local/lib/boson/playground/objects/builtin.bo
string=/usr/local/lib/boson/playground/objects/string.bo
io=/usr/local/lib/boson/playground/objects/io.bo
```

`_io_sys`, `_heap`, and `_init` are linkable runtime internals, not
user-importable packages. For example, `io.bo` may depend on `_io_sys.bo`,
and `bld` may receive `_io_sys.bo`, but submitted source cannot write
`import "_io_sys"` because `_io_sys` is absent from the static `importcfg`.

If the playground later exposes test-only packages such as `pair`, they must
be compiled into the runtime object bundle and added deliberately to
`importcfg`.

### Per-run workspace

Each run creates a temporary workspace on tmpfs:

```text
work/
  main.bos
  main.bs
  main.bo
  main
```

The runtime bundle is mounted read-only at a predictable path. The dispatcher
does not copy runtime objects or generate per-run import configuration.

### Stages

Per-run compilation:

1. Write the submitted source to `main.bos`.
2. Run `bosc -importcfg=/toolchain/playground/importcfg -o main.bs main.bos`.
3. Run `bas -o main.bo main.bs`.
4. Run `bld -o main main.bo <runtime link bundle objects>`.
5. Execute `main` inside the sandbox runner.
6. Optionally run `bdump main.bo` for the Object tab.
7. Optionally run `bosc -listimports main.bos` for the Imports tab. This is
   informational only; it is not part of compilation or access control.

The static `importcfg` is intentionally a superset of what any one submitted
program imports. It is an index of user-importable packages, not a per-run
dependency list.

Stages 2-5 are wrapped by the sandbox. The dispatcher captures argv, exit
code, stdout, stderr, duration, and produced artifact paths for every
command.

### Worker pool

- The HTTP frontend hands runs to a dispatcher that maintains a fixed-size
  worker pool. Default size = CPU count; configurable via flag.
- Excess requests queue with a wall-clock deadline. Queue-time overrun
  returns 503 Busy with a Retry-After header.
- Workers spawn fresh per request; there is no in-process caching between
  runs of user code.
- Compilation stages (bosc/bas/bld) run inside the same sandbox boundary as
  user code execution — they are not trusted; they consume user-controlled
  input.

### Output capture

- stdout and stderr captured into bounded ring-style buffers.
- On overflow, set a `truncated: true` flag, keep accepting bytes until the
  hard cap, then close the pipe.
- A separate "killed" flag is set when the sandbox kills the run (timeout or
  resource overrun).

### Exit categorization

The `status` field in the run response is one of:

- `ok` — every stage succeeded; program exited zero.
- `compile_error` — `bosc` returned non-zero.
- `assemble_error` — `bas` returned non-zero.
- `link_error` — `bld` returned non-zero.
- `runtime_error` — program returned non-zero or was killed by a runtime
  trap (bounds check, nil assert).
- `timeout` — sandbox killed the run for wall-clock overrun.
- `killed` — sandbox killed the run for a non-timeout limit (OOM, pid
  exhaustion, output overrun).
- `internal_error` — playground bug. The structured response includes the
  failing stage name; client surfaces a generic message.

## Sandbox

The run sandbox is the hardest part and should be treated as product-critical,
not an implementation detail.

### Architecture

A small dedicated runner binary (`bplay-runner`) sets up isolation primitives
and execs the target. The dispatcher invokes the runner with arguments:

```text
bplay-runner --workdir /tmp/run-XXX \
             --argv main \
             --stdin-file /tmp/run-XXX/stdin \
             --limits cpu=2s,wall=5s,mem=64MiB,pids=8,out=64KiB
```

The runner is a single static Go binary. It does not link to the toolchain.

### Linux primitives

- **Mount namespace** with a private root. Bind mounts:
  - `/toolchain` → read-only mount of the prebuilt toolchain image
    (binaries + playground runtime bundle).
  - `/work` → the per-run workspace, writable.
  - No `/proc`, no `/sys`, no `/dev` beyond `/dev/null`.
- **User namespace** mapping the runner to a non-privileged pseudo-uid; the
  outer process runs as an unprivileged service account.
- **PID namespace** so the runner is pid 1 and can reap children.
- **Network namespace** with no interfaces — not even loopback.
- **UTS** and **IPC** namespaces for completeness.
- **cgroups v2** for memory.max, pids.max, cpu.max.
- **seccomp-bpf** allowlist tailored to the runtime's actual syscall surface:
  `read`, `write`, `mmap`, `munmap`, `brk`, `rt_sigreturn`, `exit`,
  `exit_group`, plus what `bosc`/`bas`/`bld` need to compile (open, openat,
  close, fstat, lseek, etc.). Every other syscall returns `EPERM`. The
  allowlist lives in `bplay-runner` source and is reviewed when runtime
  changes touch syscalls.

The compile stages run under the same sandbox profile as user execution; they
are convenience-wrapped, not privileged.

### Concrete resource limits

Defaults (configurable per deployment):

| Limit                    | Compile stages | User execution |
| ------------------------ | -------------- | -------------- |
| Wall clock               | 3 s            | 5 s            |
| CPU time (RLIMIT_CPU)    | 3 s            | 2 s            |
| Memory (cgroup memory.max) | 256 MiB      | 64 MiB         |
| Pids (cgroup pids.max)   | 16             | 8              |
| Output bytes (combined)  | 64 KiB         | 64 KiB         |
| Open files               | 64             | 32             |
| File size (RLIMIT_FSIZE) | 1 MiB          | 1 MiB          |
| Stack                    | 8 MiB          | 8 MiB          |

Total per-request wall budget: 12 s including queue time. Clients should not
retry on `timeout`.

### Kill behavior

- Wall-clock deadline expiry → kill cgroup, drain output buffers, mark
  `timeout`.
- OOM → mark `killed` with `reason: oom`.
- Output overrun → close pipe, allow process to finish (or kill at hard
  wall deadline), mark `truncated`.

### Hardening for public exposure

Namespaces + seccomp + cgroups is enough for a private/team deployment behind
auth. For a publicly reachable instance, recommend wrapping the runner in a
microVM boundary (Firecracker or gVisor) to harden against kernel surface
bugs. The runner protocol is identical between the two backends; the
selection is a deployment flag.

The threat model document in `bplayd/SECURITY.md` (to be written alongside
the runner) should enumerate trust boundaries and assumed-broken surfaces.

## API

The backend exposes a small HTTP API. All responses are JSON. Endpoints below
return `4xx` on client errors and `5xx` on internal errors, with body
`{"error": {"code": "...", "message": "..."}}`.

### POST /api/run

```text
{
  "source": "package main\n...",
  "stdin": "",
  "example": "",
  "showAssembly": true,
  "showObject": false,
  "showImports": true,
  "runtimeVersion": ""
}
```

`runtimeVersion` is optional; if empty, the active toolchain version is used.
v1 may only accept the active version.

```text
200 OK
{
  "toolchain": "git:<commit>",
  "status": "ok|compile_error|assemble_error|link_error|runtime_error|timeout|killed|internal_error",
  "steps": [
    {"name":"bosc","argv":["bosc",...],"exitCode":0,"stdout":"...","stderr":"...","ms":12}
  ],
  "diagnostics": [
    {"file":"main.bos","line":3,"col":8,"severity":"error","message":"...","context":["..."]}
  ],
  "artifacts": {
    "assembly": "...",
    "object": { "...": "structured bdump" },
    "imports": ["builtin", "_init", "_heap", "string"]
  },
  "program": {
    "stdout": "...",
    "stderr": "...",
    "exitCode": 0,
    "truncated": false,
    "killed": false,
    "ms": 4
  }
}
```

### POST /api/snippet

Store a content-addressed snippet:

```text
{
  "source": "...",
  "stdin": "",
  "example": ""
}
```

```text
200 OK
{ "id": "ab12cd34ef56" }
```

ID derivation:

```text
id = first 6 bytes of sha256(toolchain-major || source || stdin || example-id)
hex-encoded
```

Collisions are checked on store and fall back to a longer id if needed.

### GET /api/snippet/{id}

Returns the stored snippet contents, including the toolchain commit at save
time.

### GET /api/examples and GET /api/examples/{id}

List metadata and fetch a single example payload.

### GET /api/toolchain

```text
{
  "commit": "...",
  "buildTime": "2026-05-30T12:00:00Z",
  "runtimeObjects": ["builtin","_init","_heap","_io_sys","string","io"],
  "userImportablePackages": ["builtin","string","io"]
}
```

### GET /healthz, GET /readyz

Liveness (process responsive) and readiness (runtime bundle present, static
`importcfg` readable, workers ready).

### CORS

Same-origin only for the public deployment. Local development may enable
permissive CORS via a flag.

### Idempotency and caching

`/api/run` is effectively idempotent given identical input. The dispatcher
may cache responses by `sha256(input || toolchain-commit)` with a short TTL;
optional, off by default.

## Snippet sharing and storage

- IDs are content-addressed (above). Same content + same toolchain version =
  same id.
- Backing store is a pluggable interface (`Get(id) → snippet`,
  `Put(snippet) → id`). v1 default: local filesystem under
  `/var/lib/bplayd/snippets/<id>.json`. Production deployments can swap in
  S3 or GCS without touching the dispatcher.
- Snippet shape: `{ source, stdin, example, toolchainCommit, createdAt }`.
  No author, no edit history.
- Per-snippet size cap: 32 KiB source + 32 KiB stdin.
- Retention: indefinite for low volume. A soft retention policy can prune
  unreferenced snippets older than one year if storage pressure appears.
- Snippets are served with `X-Robots-Tag: noindex`. There is no listing
  endpoint.

## Examples

Examples are checked into the repository so the playground, the tour, and
tests can share them. Format:

```text
playground-examples/
  hello/
    main.bos
    manifest.json
  arithmetic/
    main.bos
    manifest.json
```

`manifest.json`:

```json
{
  "title": "Hello, Boson",
  "description": "Print a string via string.puts.",
  "expectedStdout": "hello, world\n",
  "stdin": "",
  "tags": ["intro"]
}
```

Initial set:

- Hello world with `string.puts`.
- Integer arithmetic and casts.
- Slices and bounds checks.
- Structs.
- Type aliases and methods.
- Ownership and `dispose`.
- Nullable pointer flow narrowing.
- Interface dispatch.
- Values types.

CI loads every example, runs it, and asserts `expectedStdout` matches. A
drift fails the build.

The tour (per [PROPOSAL_boson_tour.md](PROPOSAL_boson_tour.md)) maintains
its own lesson set independently. The playground and the tour are separate
services with separate content; they share architecture, not examples.

## Diagnostics

`bosc` already emits `file:line:col: message` plus a five-line source snippet
and caret arrow. The playground should parse stderr from each stage into
structured diagnostics:

```text
{ file, line, col, severity, message, context: [...] }
```

The synthetic source is named `main.bos` so positions point to the editor
buffer directly. Diagnostics from prebuilt runtime packages should not occur
on the request path; if bundle validation catches one at startup or image
build time, it is an operator-facing error rather than a user diagnostic.

Editor markers:

- Red squiggle on the reported range (column to end-of-line if no length).
- Hover tooltip shows the message.
- A diagnostics list at the top of the output pane is clickable to jump.

When `bosc` emits its color-coded source snippet, the playground strips ANSI
codes before parsing — the structured `context` array is plain text.

## Caching and toolchain versioning

- The toolchain commit is embedded in `bplayd` at build via
  `-ldflags '-X main.toolchainCommit=<sha>'`.
- The playground runtime bundle is built with the toolchain image and copied
  into a predictable read-only directory, for example
  `/usr/local/lib/boson/playground/`. Multiple versions can coexist in
  versioned directories if a deployment chooses to retain old toolchains.
  (v1: only the active version is supported.)
- Examples are embedded in the binary via `go:embed` and reloaded on
  startup.
- Snippets store the toolchain commit they were saved against. Loading a
  snippet from an older commit on a newer toolchain shows a warning chip;
  re-running compiles against the current toolchain.

## Observability

- Per-request structured JSON logs: request id, status, stage timings, exit
  codes, source size, response size, IP hash. **No source content in logs by
  default.**
- Prometheus metrics: request rate, status distribution, queue depth,
  sandbox kills (with reason), runner setup time, p50/p95/p99 stage timings
  per stage.
- Panics in `bplayd` log at error level with stack. Runtime errors from user
  code do not log as errors (they are expected outcomes).
- The frontend reports nothing back to the server beyond what's required for
  a run. No analytics.

## Deployment

- Container image: distroless or minimal Alpine. Contains `bplayd`,
  `bplay-runner`, the toolchain binaries, the prebuilt playground runtime
  bundle, and the embedded frontend assets. Stateless apart from snippet
  storage.
- Default port 8086 (configurable). TLS termination expected at a reverse
  proxy.
- Readiness: `/readyz` returns 200 once the runtime bundle and static
  `importcfg` are present and the worker pool is warm.
- Liveness: `/healthz` returns 200 while the dispatcher is responsive.
- Required kernel features: cgroups v2, user namespaces, seccomp-bpf. The
  image refuses to start on a host that does not meet the prerequisites.

## Local development

- `bplayd -mode=local` runs without sandbox primitives, executing stages via
  `os/exec` with simple `RLIMIT_AS` and `RLIMIT_CPU` only. Logs a prominent
  warning at startup. Intended for UI iteration on a workstation; not for
  any deployment.
- `make playground-dev` builds the toolchain, builds the playground runtime
  bundle, and starts `bplayd -mode=local` on `localhost:8086`.
- Frontend hot reload via a small file watcher; no full webpack pipeline
  required.
- The runner can be exercised in isolation: `bplay-runner --workdir ...`
  takes a prebuilt binary and runs it under the configured sandbox, useful
  for sandbox-level integration tests.

## Security and abuse prevention

- Per-IP rate limits: 30 runs/min, 10 snippet creations/min. Leaky bucket.
- Optional captcha gate triggered after threshold (off by default; behind a
  flag).
- Source size cap: 64 KiB. Larger requests rejected with 413.
- Stdin size cap: 16 KiB.
- CSP: strict; no inline scripts. Bootstrap inline via a sha256-pinned
  inline script if needed.
- CORS: same-origin only for the public deployment.
- HSTS, X-Content-Type-Options, Referrer-Policy: set at the reverse proxy.
- No file uploads in v1.
- Public snippet pages carry `X-Robots-Tag: noindex`.

## Implementation plan

Milestones with explicit scope:

**M1 — Backend skeleton.** `cmd/bplayd` with `/api/run` that wraps
`bosc`/`bas`/`bld` in a temp dir, no sandbox, returns the structured
response. Local-only. Single example wired through. Uses a prebuilt
playground runtime bundle and static `importcfg`; no per-request runtime
compilation or import closure resolution.

**M2 — Sandbox runner.** `cmd/bplay-runner` with namespaces, cgroups,
seccomp. Dispatcher switches `/api/run` to invoke the runner. Reach an
end-to-end run of the hello-world example under full isolation.

**M3 — Minimal frontend.** HTML/JS with CodeMirror, run button, output
pane. Wires up to backend. No artifact tabs yet.

**M4 — Examples and share.** `playground-examples/` directory, embed at
build time, `/api/examples` endpoints, `/api/snippet` with filesystem
backend.

**M5 — Artifact tabs.** Assembly, Object (structured bdump), Imports, Link.

**M6 — Diagnostics.** Parse `bosc` stderr into structured diagnostics,
surface as editor markers.

**M7 — Public hardening.** Rate limiting, optional captcha hook,
observability (logs + metrics), container image, deploy story, security
review.

**M8 — Tour integration.** Add the tour endpoints from
[PROPOSAL_boson_tour.md](PROPOSAL_boson_tour.md) once the playground
backend is stable.

## Testing

- Unit tests for command planning (stage argv computation, runtime link
  bundle ordering, static `importcfg` validation) without executing user
  code.
- Integration tests against the real toolchain build: every checked-in
  example must run cleanly with matching stdout.
- Sandbox tests:
  - Verify the runner refuses syscalls outside the allowlist (a planted
    fork must fail with `EPERM`).
  - Verify wall-clock and CPU timeouts kill correctly.
  - Verify OOM is reported as `killed` with `reason: oom`.
  - Verify output overrun marks `truncated` and continues capture up to the
    hard cap.
- Fuzz the request handler with malformed JSON and oversized inputs.
- End-to-end browser testing is deferred. Playwright (or similar) would
  add a separate browser download outside the npm dependency boundary,
  which conflicts with the "one new tool" rule for prerequisites.
  Manual testing covers the UI flow in v1; revisit only if regressions
  become frequent.
- Run the existing `cmd/bosc/tests` and `cmd/bas/tests` against the same
  toolchain build used by the playground image. The image build fails if
  either suite drifts.

## Risks

- **Sandbox escape.** The playground is a remote code execution surface by
  design. Mitigation: layered defenses (namespaces + seccomp + cgroups,
  microVM optional for public exposure); least-privilege runner; no shared
  writable filesystem between requests; security review before public
  launch; threat model document.
- **Toolchain regression in the image.** A broken compiler ships to every
  user at once. Mitigation: CI runs examples and the full test suites on
  every image build; image build fails on any drift.
- **Capacity exhaustion.** A viral link can saturate the worker pool.
  Mitigation: per-IP rate limit, global concurrency cap, 503 with
  Retry-After, response caching for identical inputs.
- **Stale snippets.** A snippet stored against an older toolchain may stop
  compiling on a newer one. Mitigation: pin the toolchain commit on save;
  surface a warning chip on load; v2 supports re-running against the saved
  commit by retaining old toolchain versions.
- **User-import surface drift.** A new runtime package added in `runtime/`
  does not automatically become user-importable. Mitigation: the static
  playground `importcfg` is generated from an explicit user-importable package
  list; CI fails if that list references a missing object or if the runtime
  bundle is missing objects required by user-importable packages.

## Future directions

- **In-browser compilation via WebAssembly.** Build `bosc`/`bas`/`bld` as
  WASM and run inside the browser. This removes the compile-time sandbox
  problem entirely; execution still needs a server runner or a WASI VM.
  Tracked as a v3+ goal.
- **Multi-file projects** with an explicit dependency manifest.
- **Language server** using `bosc` as a daemon: autocomplete, jump-to-def,
  hover types.
- **Cross-architecture targets** once
  [PROPOSAL_arm64_backend.md](PROPOSAL_arm64_backend.md) lands. The run
  request can carry a `target` field selecting `linux/amd64` or
  `linux/arm64`; the runner image needs the cross binaries and a playground
  runtime bundle for each target.
- **Streaming output** via SSE or WebSocket for longer-running programs (if
  the wall budget is ever raised).
- **Embeddable widget** for documentation pages: a small `<script>`-tag
  embed that mounts a read-only or limited playground inline.

## Relationship to other proposals

- [PROPOSAL_boson_tour.md](PROPOSAL_boson_tour.md) — the tour is a
  separately deployed service built on the same architecture: it reuses
  the playground's compile/sandbox/run pipeline (likely as a shared
  internal library or by running its own instance of the same backend),
  but serves its own content — lessons, prose, and checks — independent
  of the playground's example set. The two should not be co-deployed or
  share a content directory.
- [PROPOSAL_arm64_backend.md](PROPOSAL_arm64_backend.md) — once arm64
  lands, the playground can expose architecture as a per-run setting.
- [PROPOSAL_macho_revive.md](PROPOSAL_macho_revive.md) — playground
  targets ELF64/Linux for execution. macOS object output is interesting
  for downloadable artifacts, not for the sandboxed runner.

## Open questions

- Should snippets be tied to an exact git commit or a named release
  channel (`stable`, `nightly`)?
- Should the UI include `bdump` by default, or hide it behind an advanced
  tab?
- Should the first public version use containers/cgroups alone or wrap the
  runner in a microVM (Firecracker/gVisor) boundary?
- Should runtime package compilation remain image-build-only, or should
  local development also support rebuilding the playground runtime bundle on
  server startup? Image-build-only is simpler and matches deployment.
- Should the playground live in this repository or a sibling repo?
  In-repo simplifies versioning; out-of-tree allows an independent release
  cadence.
- Should snippet ids be content-addressed only, or include a
  server-generated short id independent of content for rotation?
- At what point does the HTTP API contract become stable enough for the
  tour to depend on it?
- Should the playground build a Lezer grammar for Boson source as part of
  v1, or ship with regex highlighting and upgrade later?
- Should `_io_sys` ever be exposed in the static `importcfg` (gated behind a
  "trusted host" flag for local development), or remain permanently
  non-user-importable?
- Should the frontend stay vanilla TypeScript, or adopt a small framework
  (Svelte, Preact) once the UI grows? Revisit when the tour client is
  added.
