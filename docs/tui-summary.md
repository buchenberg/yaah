# TUI Component System — Design Summary

## Background

The current TUI rendering in `render.go` has repetitive styling patterns — the same
padding, margin, and width logic repeated across many functions. Styles are applied
inline with manual calculations, making global changes labor-intensive.

## Proposed Solution: React-like Component System

A `Component` abstraction that encapsulates content, styling properties, and
composition. This would:

1. Centralize padding/margin/width logic in a shared interface
2. Allow global style changes in one place
3. Make components independently testable
4. Reduce code duplication in `render.go` and `View()`

## Design Artifacts

- **`tui-component-design.md`** — interface design, component types, migration plan
- **`tui-refactoring-example.md`** — side-by-side before/after comparisons for each TUI element

## Key Design Decisions (Unresolved)

1. **Immutability**: Should component builders return new instances or mutate in place?
2. **Dynamic content**: How to handle streaming/thinking content in components?
3. **Integration**: Fully replace `View()`/`renderMessages()` or create additive wrappers?
4. **Theme integration**: How tightly coupled should components be to the existing `theme.go`?

## Status

**Design phase.** These documents capture the problem analysis and proposed API.
Implementation has not been merged into the main TUI code.
