# Design docs

Docs are organized **by lifecycle state — the directory is the source of
truth for a doc's status.** The full convention (lifecycle, the `## History`
log, and the DESIGN.md-is-truth policy) is in
[`../CLAUDE.md`](../CLAUDE.md) § Documentation and design docs.

The always-current entry points live at the repo root: `CLAUDE.md`,
`DESIGN.md` (the language/toolchain reference), and `TODO.md` (backlog).

## `implemented/` — Implemented

Designs for features that have shipped and are tested. **Frozen historical records** — the current spec lives in `../DESIGN.md`; these capture the design and its rationale as of shipping.

- [`DESIGN_prefix_type_syntax.md`](implemented/DESIGN_prefix_type_syntax.md) — DESIGN: Prefix type syntax + the mutability model
- [`DESIGN_return_alias_engine.md`](implemented/DESIGN_return_alias_engine.md) — Design note: return-alias inference engine (rebuild)
- [`PLAN_values_with_static_data.md`](implemented/PLAN_values_with_static_data.md) — Implementation Plan: `values` Types With Static Projections
- [`PROPOSAL_adts_and_switch.md`](implemented/PROPOSAL_adts_and_switch.md) — Proposal: payload-carrying `values` (ADTs) + a variant `switch`
- [`PROPOSAL_boson_playground.md`](implemented/PROPOSAL_boson_playground.md) — Proposal: Boson Playground
- [`PROPOSAL_boson_tour.md`](implemented/PROPOSAL_boson_tour.md) — Proposal: Boson Tour
- [`PROPOSAL_fmt_package.md`](implemented/PROPOSAL_fmt_package.md) — Proposal: `fmt` Formatting Package
- [`PROPOSAL_immutable_by_default.md`](implemented/PROPOSAL_immutable_by_default.md) — Proposal: Immutable-by-default bindings (`:=`), drop `const`
- [`PROPOSAL_interface_borrow_contracts.md`](implemented/PROPOSAL_interface_borrow_contracts.md) — Proposal: Interface Borrow Contracts (`from` clauses)
- [`PROPOSAL_owned_field_move.md`](implemented/PROPOSAL_owned_field_move.md) — Proposal: Moving Owned Fields Out of Aggregates (typestate-lite)
- [`PROPOSAL_pub_visibility.md`](implemented/PROPOSAL_pub_visibility.md) — Proposal: `pub` Visibility Modifier
- [`PROPOSAL_return_alias_inference.md`](implemented/PROPOSAL_return_alias_inference.md) — Proposal: Inferred Return-Parameter Aliasing
- [`PROPOSAL_values_with_static_data.md`](implemented/PROPOSAL_values_with_static_data.md) — Proposal: `values` Types With Explicit Projections
- [`PROPOSAL_variadics_and_assertion.md`](implemented/PROPOSAL_variadics_and_assertion.md) — Proposal: Variadics, the `any` Interface, and Type Assertion

## `proposed/` — Proposed

Designs not yet built — drafts, ideas, accepted-but-unbuilt proposals.

- [`DESIGN_10_field_provenance.md`](proposed/DESIGN_10_field_provenance.md) — Scoping: #10 — field-buried borrow escapes the frame
- [`DESIGN_origin_generations.md`](proposed/DESIGN_origin_generations.md) — Design note — Origin generations
- [`PROPOSAL_arm64_backend.md`](proposed/PROPOSAL_arm64_backend.md) — Proposal: arm64 Backend for bas and bosc
- [`PROPOSAL_c_transpiler.md`](proposed/PROPOSAL_c_transpiler.md) — Proposal: Boson→C transpiler backend (portable, optimizing; sibling of the .bs backend)
- [`PROPOSAL_interface_equality.md`](proposed/PROPOSAL_interface_equality.md) — Proposal: Equality Comparison for Interface Values
- [`PROPOSAL_macho_revive.md`](proposed/PROPOSAL_macho_revive.md) — Proposal: Revive Mach-O Executable Support

## `reference/` — Reference

Living analysis that isn't a feature proposal: coverage models, audits, reports.

- [`AUDIT_codegen_check_segregation.md`](reference/AUDIT_codegen_check_segregation.md) — Census of semantic checks leaked into codegen (semantic vs lowering); hoist worklist for a from-AST backend
- [`AUDIT_memory_backed_values.md`](reference/AUDIT_memory_backed_values.md) — Coverage audit — memory-backed value mechanics
- [`COVERAGE_MATRIX.md`](reference/COVERAGE_MATRIX.md) — Test coverage model — invariants, equivalence classes, the audit
- [`REPORT_array_slice_provenance.md`](reference/REPORT_array_slice_provenance.md) — REPORT: Indexed-aggregate provenance model
