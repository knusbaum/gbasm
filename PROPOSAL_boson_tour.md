# Proposal: Boson Tour

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
aliases with methods, interfaces, and values types.

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
  [PROPOSAL_boson_playground.md](PROPOSAL_boson_playground.md). This
  should live as a reusable Go library (`internal/bpipeline` or similar)
  that both `bplayd` and `btourd` link against. Alternatively, the tour
  service can shell out to its own instance of the playground backend
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
18. Owned fields.
19. Borrowed pointers.
20. Common compile-time ownership errors.

### Abstraction

21. Type aliases.
22. Methods on named types.
23. Function-pointer types.
24. Interfaces and static dispatch table generation.
25. Cross-package imports at a conceptual level.

### Values Types

26. Declaring `values` types.
27. Cases and projection casts.
28. Values comparisons.
29. Values in interfaces or structs.

### Runtime Packages

30. `string` output helpers.
31. `io.FD` and typed file operations.
32. `_heap` through the allocator interface, if exposed in user-facing form.

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

The tour depends on the playground proposal having extracted the
compile/sandbox/run pipeline into a reusable library. That extraction is
playground work; the tour starts once it exists.

1. Define the lesson file format and add a tiny lesson loader.
2. Build 8-10 initial lessons covering basics, data, pointers, and
   ownership.
3. Stand up `cmd/btourd` linking the shared pipeline library; serve the
   lesson index and a single lesson run endpoint.
4. Add the tour verifier to CI.
5. Build the tour frontend (lesson navigation, prose, embedded editor and
   output) using the same frontend tooling as the playground: npm + esbuild
   + CodeMirror 6, no additional build-time tools introduced. The editor
   component can be reused from the playground if it is extracted into a
   shared module.
6. Expand lessons through interfaces, values types, and runtime packages.
7. Add optional checks once plain stdout lessons are stable.

## Open questions

- Should compile-fail lessons appear inline in the main path, or in a
  separate "diagnostics" section?
- Should the tour support multiple source files once Boson supports
  multi-package projects?
- Should lessons pin to exact language versions after Boson has releases?
- Should the shared pipeline live in this repository alongside the
  toolchain, or move to a sibling repo if both `bplayd` and `btourd`
  develop independent release cadences?
- Should the tour frontend share an editor module with the playground
  frontend, or stay independent so each can iterate on UI without
  coordination?

