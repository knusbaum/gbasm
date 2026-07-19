# Proposal: Boson Tour

## History

- 2026-07-19 — Filed under `docs/implemented/` in the docs reorganization; the feature had already shipped. Earlier history predates this log.

## Summary

Build an interactive walkthrough of Boson's language features, similar in
spirit to `go.dev/tour`. The tour should teach the language through runnable
lessons built on the same compile/sandbox/run architecture as the Boson
playground (see [PROPOSAL_boson_playground.md](PROPOSAL_boson_playground.md)).

The tour is a separate service from the playground. It serves its own
content — lessons, prose, and checks — and ships its own frontend. What it
reuses is the playground's run pipeline (likely as a shared library or by
running its own instance of the same backend binary), not the playground's
deployment, examples, or UI.

The tour is not just documentation with examples. Each lesson should have a
small editable program, a focused explanation, expected output, and optional
checks that confirm the learner exercised the intended concept.

## Motivation

`DESIGN.md` is the authoritative language reference, but it is dense and aimed
at implementers. New users need a guided route through Boson's unusual parts:
ownership obligations, nullable pointer flow, write-through mutability, type
aliases with methods, interfaces, values types, and sum types with `switch`
dispatch.

A tour gives the project a stable teaching surface while keeping the reference
document focused on semantics.

## Goals

- Teach Boson with short runnable programs.
- Reuse the playground's compile/sandbox/run architecture so every lesson
  runs through the real toolchain under the same isolation guarantees.
- Cover the feature set described in `DESIGN.md`, including ownership and
  current runtime packages.
- Provide a lesson format that can be tested in CI.
- Allow lessons to double as regression tests for the public learning path.

## Non-goals

- Replacing `DESIGN.md`.
- Teaching compiler internals.
- Supporting arbitrary multi-file projects in the first version.
- Adding accounts, progress sync, or certificates.
- Sharing a deployment, frontend, or content directory with the playground.

## Relationship to the playground

The tour and the playground are separate services. They share architecture,
not content or deployment.

What is shared:

- The compile/sandbox/run pipeline — `bosc`, `bas`, `bld`, the runtime
  cache, and the sandbox runner described in
  [PROPOSAL_boson_playground.md](PROPOSAL_boson_playground.md). This now
  lives as a reusable Go library, `internal/bpipeline`, that both
  `bplayd` and `btourd` link against. (`bplayd` was refactored onto it
  with no behavior change; the runner protocol moved with it.) The tour
  links the library directly rather than shelling out to a playground
  binary.
- The diagnostic-parsing logic, the runtime allowlist, and the resource
  limit defaults.
- The toolchain version pinning mechanism.

What is **not** shared:

- Content. The tour serves lessons; the playground serves examples and
  free-form snippets. Each has its own directory in the repository.
- Frontend. The tour ships its own UI focused on lesson navigation and
  prose; the playground ships a free-form editor.
- Deployment. The tour and playground are separate binaries, separate
  container images, separate hostnames.
- HTTP API. The tour exposes its own endpoints (below); it does not add
  routes to `bplayd`.

The playground is the free-form editor. The tour is the curated path.

## Lesson format

Lessons should live in the repository as data files so changes are reviewable.
A simple directory shape is enough:

```text
tour/
  01-basics/
    01-hello/
      lesson.md
      main.bos
      expected.stdout
      check.json
```

`lesson.md` should be prose with a small amount of front matter:

```text
---
title: Hello, Boson
section: Basics
order: 1
---
```

`check.json` can start small:

```json
{
  "kind": "stdout",
  "contains": ["hello"]
}
```

Future checks can include compile-fail lessons, AST-level checks, or expected
diagnostics. The first version should avoid clever hidden tests; plain expected
output is easier to understand.

## Proposed lesson sequence

The bundle available to tour programs exposes `builtin`, `io`, and `fmt`
as importable packages; lessons use `fmt` for output (the old `string`
helper package is not in the tour bundle).

### Basics

1. Hello world and `package main`.
2. Functions and `main`.
3. Variables, constants, and type inference.
4. Integer types, literals, casts, and signed versus unsigned behavior.
5. Control flow with `if`, `for`, `break`, and `continue`.

### Data

6. Arrays and slices.
7. String literals as `byte[]`.
8. Struct declarations and anonymous structs.
9. Multi-value returns and destructuring.
10. File-scope globals and static initialization.

### Pointers and Safety

11. Pointers and address-of.
12. Nullable pointers, flow narrowing, and postfix `?`.
13. `mut` on pointers and slices.
14. Binding mutability with `const` and `var`.
15. Bounds checks and runtime traps.

### Ownership

16. Owned values and obligations.
17. `dispose`.
18. Owned fields — including moving an owned field out of an aggregate and
    re-initializing it (the partial-decompose / value-field-move feature).
19. Borrowed pointers.
20. Common compile-time ownership errors.

### Abstraction

21. Type aliases.
22. Methods on named types.
23. Function-pointer types.
24. Interfaces and static dispatch table generation.
25. Nullable interfaces (`?I`), type assertion, and interface-to-interface
    widening.
26. Interface borrow contracts (`from(...)`) — what a method promises about
    pointers it returns.
27. Cross-package imports at a conceptual level.

### Sum Types and Dispatch

28. The `any` type and the type-switch (`switch (v x.(type))`) — a dynamic-type
    escape hatch, and dispatching on a value's runtime type.
29. Declaring `values` types (tag-only closed symbolic sets).
30. Cases and projection casts.
31. Values comparisons.
32. Values in interfaces or structs.
33. Payload variants — a variant *is* a struct, and the tag selects which
    (ADTs): declaration (`FILE_INFO{ … }`), construction, and field access.
34. `switch` over a `values` type — the *second mode* of `switch`: exhaustive
    variant dispatch, per-case narrowing, field mutation, and payload ownership
    (moving an owned payload out, the destructor pattern). See
    [`PROPOSAL_adts_and_switch.md`](PROPOSAL_adts_and_switch.md).

### Runtime Packages

35. `fmt` formatted output (`print`/`printf` and the format verbs).
36. `io.FD` and typed file operations.
37. `_heap` through the allocator interface, if exposed in user-facing form.

## UI

The first screen should be the lesson itself:

- Left or top: lesson text and navigation.
- Right or bottom: editor and output.
- Artifact tabs available but secondary.
- Run and Reset controls near the editor.
- Previous/Next navigation.

The lesson text should explain the feature being used, not the website. Avoid
in-app instructional clutter about the editor unless a control is unusual.

## Backend

The tour ships its own backend, `cmd/btourd`, separate from the
playground's `bplayd`. It links the shared run-pipeline library described
above and adds tour-specific endpoints:

```text
GET  /api/tour
GET  /api/tour/{section}/{lesson}
POST /api/tour/{section}/{lesson}/run
```

`GET /api/tour` returns the lesson index (sections, ordered lesson titles,
ids). `GET /api/tour/{section}/{lesson}` returns the lesson payload:
`lesson.md` rendered or raw, `main.bos`, and any auxiliary metadata
(expected stdout, hints) the UI needs.

The run endpoint invokes the shared pipeline on the submitted source, then
applies the lesson's check and returns:

```json
{
  "run": { "...": "same shape as the playground run response" },
  "check": {
    "passed": true,
    "message": ""
  }
}
```

The `run` sub-object is shape-compatible with `bplayd`'s `/api/run`
response — that compatibility comes from sharing the pipeline library, not
from forwarding to a playground instance.

Lesson content is loaded from a `tour/` directory embedded into `btourd` at
build time via `go:embed`. Updating lessons means rebuilding and
redeploying `btourd`; no shared state with the playground is required.

## Content rules

- Every lesson program must compile or intentionally fail in a documented way.
- Every successful lesson should have expected stdout, even if empty.
- Lessons should be short enough to fit on one screen before editing.
- Concepts should be introduced before they are required, except where the
  lesson explicitly says it is a preview.
- Use real Boson syntax and runtime packages; do not invent pseudo-code.

## CI

Add a tour verifier that:

1. Discovers all lessons.
2. Validates front matter and required files.
3. Compiles each `main.bos` through `bosc`.
4. Assembles and links successful lessons.
5. Runs them under the same timeout/output limits as the playground.
6. Compares stdout or expected diagnostics.

This verifier should run alongside existing compiler and assembler tests. The
tour then becomes a living compatibility contract for the public language
walkthrough.

## Implementation plan

The tour depended on extracting the compile/sandbox/run pipeline into a
reusable library. That extraction is **done**: the pipeline now lives in
`internal/bpipeline`, and `bplayd` was refactored onto it with no behavior
change. The remaining steps below are the rig itself, of which 1–5 are
implemented:

1. **Done.** Lesson file format + loader in `internal/tour` (front matter,
   `check.json` with `stdout` and inline `diagnostic` kinds). Content lives
   at repo-root `tour/`, embedded via `go:embed` (`package tourcontent`).
2. Build out the lesson sequence. Two sample lessons exist today
   (`01-basics/01-hello`, `02-ownership/01-use-after-move`) to prove the
   rig; the full curriculum is follow-up content work.
3. **Done.** `cmd/btourd` links `internal/bpipeline` + `internal/tour` and
   serves the index, per-lesson payload, and run endpoints.
4. **Done.** A tour verifier rides `go test ./...` (the `tour` package
   test compiles and runs every lesson through the pipeline and applies
   its check; it skips cleanly when the toolchain/bundle are not built).
5. **Done.** The tour frontend is hand-rolled vanilla JS served via
   `go:embed` (`cmd/btourd/static/`), matching the playground — which is
   itself vanilla JS, not npm/esbuild/CodeMirror. No frontend build step
   is introduced. A richer editor (e.g. CodeMirror) would be a separate,
   opt-in decision affecting both services.
6. Expand lessons through interfaces, values types, sum types (`switch`), and
   runtime packages.
7. Add richer checks once plain stdout/diagnostic lessons are stable.

## Open questions

Resolved:

- **Compile-fail lessons appear inline in the main path.** A lesson sets
  `check.json` kind `diagnostic` instead of supplying `expected.stdout`;
  the check asserts the program fails to compile with a message containing
  the listed substrings. Errors are taught where the concept lives.
- **The shared pipeline lives in this repository** as `internal/bpipeline`,
  alongside the toolchain it wraps. Splitting to a sibling repo is
  premature while both `bplayd` and `btourd` ship from here.

Still open:

- Should the tour support multiple source files once Boson supports
  multi-package projects?
- Should lessons pin to exact language versions after Boson has releases?
- Should the tour frontend share an editor module with the playground
  frontend? Today each ships its own vanilla-JS frontend independently; a
  shared module only pays off if a richer editor is adopted.

