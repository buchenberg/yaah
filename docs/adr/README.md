# Architecture Decision Records (ADRs)

This directory contains Architecture Decision Records for the yaah project.

ADRs document the "why" behind significant architectural decisions, providing context for future maintainers and helping avoid re-litigating past choices.

## Active ADRs

| Number | Title | Status | Date |
|--------|-------|--------|------|
| [ADR-001](./0001-engine-view-separation.md) | Engine-View Separation | Accepted | 2026-08-03 |
| [ADR-002](./0002-middleware-pipeline.md) | Middleware Pipeline Pattern | Accepted | 2026-08-03 |
| [ADR-003](./0003-functional-options.md) | Functional Options Pattern for Loop Configuration | Accepted | 2026-08-03 |
| [ADR-004](./0004-event-driven-architecture.md) | Event-Driven Architecture with Pub/Sub | Accepted | 2026-08-03 |

## Template

Use [0000-template.md](./0000-template.md) as a starting point for new ADRs.

## Format

ADRs follow a modified [MADR](https://adr.github.io/madr/) format:

- **Filename:** `NNNN-description.md` where NNNN is a 4-digit number (zero-padded)
- **Frontmatter:** Status, Date, Author, Related ADRs
- **Sections:** Context, Decision, Alternatives Considered, Consequences, References

## Status Definitions

| Status | Description |
|--------|-------------|
| **Draft** | ADR is being written, not yet proposed |
| **Proposed** | ADR is under review by maintainers |
| **Accepted** | ADR has been agreed upon and implemented |
| **Deprecated** | ADR has been superseded by a newer decision |
| **Superseded** | ADR has been replaced by a newer ADR |

## How to Add a New ADR

1. Copy `0000-template.md` to `NNNN-description.md` (next available number)
2. Fill in the sections
3. Open a PR for review
4. Once accepted, update this README.md

## Related Documentation

- [Architecture Overview](../architecture.md)
- [SOLID Principles Analysis](../solid-analysis.md) (generated from code review)
- [CONTRIBUTING.md](../CONTRIBUTING.md)
