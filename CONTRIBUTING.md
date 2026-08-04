# Contributing to yaah

> **Thank you for considering a contribution to yaah!**

Thanks for your interest in yaah. This document covers the boring-but-
important parts: how to file issues, what PRs we accept, and the ground
rules that keep the project vendor-free.

## Ground rules (non-negotiable)

These are the project's load-bearing principles. If a PR would violate
one of them, it will be rejected even if the code is correct.

1. **No subscription offers.** Don't add paid-only features, "premium"
   tiers, upsell flows, or affiliate links. The project makes money (if
   ever) through support, hosted add-ons, or donations. Vendoring a free
   product into a paid SaaS is fine; adding a paywall into yaah is not.

2. **No vendor lock-in.** Every feature must work with at least two
   model providers (OpenAI-compatible + at least one other). If a
   feature is Anthropic-only or OpenAI-only, it doesn't ship.

3. **No telemetry.** Don't add phone-home, analytics, or any code that
   transmits user data without explicit opt-in. The default is silent.

4. **No required accounts.** yaah must work with a local model (Ollama,
   LM Studio) and a single API key. No "sign in to continue" flows.

5. **Standards over reinvention.** When the cross-tool ecosystem
   converges on a convention (SKILL.md, AGENTS.md, ~/.agents/), adopt
   it verbatim. Diverging requires a written rationale in the PR
   description that points at the upstream discussion.

6. **Minimal config.** `~/.yaah/` stays small. If a feature needs a
   second config file, it should probably live elsewhere (in a project,
   in `~/.agents/`, or in the skill that uses it).

## How to file an issue

- **Bug reports:** include `yaah version`, your OS, the exact command
  you ran, and the full output (including any `yaah doctor` output).
- **Feature requests:** describe the use case first, then the proposed
  behavior. "I want to do X" is much more useful than "add feature Y."
- **Security issues:** see [SECURITY.md](./SECURITY.md). Don't open a
  public issue.

## How to submit a PR

1. **Fork and branch.** Branch names are short and hyphen-separated, no
   slashes or type prefixes (`session-recovery`, not `feat/session-recovery`).

2. **Conventional commits.** PR titles and commit messages follow
   `type(scope): summary`. Valid types: `feat`, `fix`, `docs`, `chore`,
   `refactor`, `test`. Scopes are optional (`cli`, `mcp`, `skills`,
   `memory`, `providers`, `docs`, etc.).

3. **Tests.** New behavior ships with a test. Bug fixes ship with a
   regression test. Run `go test ./...` locally before opening the PR;
   CI will run it again but a green local build is faster feedback.

4. **Lint clean.** CI runs `gofmt -l .` (must be empty), `go vet ./...`,
   and `staticcheck ./...`. If any of those fail, the PR will be
   blocked. Run them locally first:
   ```bash
   gofmt -l . | tee /dev/stderr
   go vet ./...
   go run honnef.co/go/tools/cmd/staticcheck@latest ./...
   ```

5. **One concern per PR.** Don't mix a bug fix with a refactor with a
   new feature. Smaller PRs review faster and merge faster.

6. **Update the docs.** If your change is user-visible, update
   `README.md` and (if it exists) the relevant section of the design
   plan. If your change is purely internal, a `docs:` line in the PR
   description is enough.

7. **Don't bump the version.** Maintainers cut releases. Your PR just
   needs to land on `main`.

## What we won't accept

- PRs that introduce a build step beyond `go build` (no codegen, no
  pre-commit hooks required, no Node.js tooling).
- PRs that add a "yaah-cloud" or any hosted service.
- PRs that reformat large amounts of code without functional change
  (run `gofmt` yourself first; we won't take a sweeping reformat PR).
- PRs that depend on a proprietary or paid-only service.

## Code of conduct

Be kind. Disagree on the merits, not the person. Assume good faith.
The maintainers reserve the right to close threads that aren't
productive.

---

## Code Style Guide

yaah follows **idiomatic Go** conventions. This section highlights the most important style guidelines.

### General Principles

1. **Be clear, not clever** - Code should be easy to understand
2. **Minimal API surface** - Expose only what's necessary
3. **Errors are values** - Don't panic for expected errors
4. **Make the zero value useful** - Default behavior should be sensible
5. **Avoid globals** - Use dependency injection

### Formatting

- Run `gofmt -w .` before committing
- Run `go vet ./...` - must be clean
- Run `staticcheck ./...` - must be clean
- Line length: Prefer < 100 chars when reasonable

### Naming

- **Packages**: Short, lowercase, singular (e.g., `agent`, `tools`, `mcp`)
- **Types**: PascalCase (e.g., `LoopConfig`, `TokenDeltaEvent`)
- **Variables**: camelCase (e.g., `loopConfig`, `tokenDeltaEvent`)
- **Functions**: camelCase, verb-noun style (e.g., `SendRequest`, `HandleEvent`)
- **Receiver names**: Use short names (1-2 letters) for common types

### Error Handling

- Use `%w` for error wrapping: `fmt.Errorf("provider error: %w", err)`
- Don't log errors in libraries - let the caller decide
- Use sentinel errors for expected, checkable conditions
- Check errors at the top - handle errors as close to where they occur as possible

### Control Flow

- **Prefer early returns** over nested if/else
- **Prefer `for...range`** over index-based loops
- Use `switch` for type assertions
- Avoid `break` with labels

### Concurrency

- Use `context.Context` for cancellation and timeouts
- Always check `ctx.Done()` in loops
- Use appropriate `sync` primitives (Mutex, RWMutex, Once, WaitGroup)
- Avoid goroutine leaks - always use `defer wg.Done()`

### Testing

- **Table-driven tests** for similar cases
- Use sub-tests to isolate test cases
- Use `t.Helper()` for helper functions in tests
- Test both happy path and error cases
- Use interfaces for mocking (not mocking frameworks)
- Run `go test ./...` before committing

### Documentation

- Every package has a package-level doc comment
- Every exported function and type has a doc comment
- Use inline comments for non-obvious logic
- Avoid comments that restate the code

---

## Adding New Features

### Adding a New Tool

1. Define the tool struct in `internal/tools/`
2. Implement the `Tool` interface (`Name`, `Description`, `Schema`, `Execute`)
3. Register in `leafTools` map in `internal/tools/tools.go`
4. Add tests in `<toolname>_test.go`
5. Verify with `go build . && ./yaah`

### Adding New Middleware

1. Define middleware type in `internal/agent/pipeline/`
2. Implement the `Middleware` interface (`Name`, `PrepareStep`, `PostModel`, `PostTool`)
3. Register in `builtinBuilders` map in `internal/agent/pipeline/config.go` (optional)
4. Add tests

### Adding a New Event Type

1. Define event struct in `internal/agent/events.go`
2. Implement `eventMarker()` method
3. Add to compile-time satisfaction checks
4. Handle in all consumers or use `NoopView`

---

## Review Checklist

This checklist is used by maintainers to review PRs. Contributors can use it to self-review before submitting.

### Code Quality

- [ ] Code follows Go conventions
- [ ] All public functions and types have doc comments
- [ ] All packages have package-level doc comments
- [ ] Code is formatted with `gofmt`
- [ ] No unused imports or variables
- [ ] Error handling is consistent and appropriate
- [ ] Context is properly propagated
- [ ] Resources are properly closed

### Testing

- [ ] New code has corresponding tests
- [ ] Tests cover happy path and error cases
- [ ] Tests pass locally (`go test ./...`)
- [ ] No regressions in existing tests

### Documentation

- [ ] README.md updated if user-facing change
- [ ] Configuration docs updated if new options
- [ ] ADR added for significant architectural decisions

### Architecture

- [ ] Follows SOLID principles
- [ ] Single Responsibility Principle
- [ ] Open/Closed Principle
- [ ] Proper separation of concerns
- [ ] No circular dependencies

### Security

- [ ] No hardcoded secrets
- [ ] Sensitive data properly handled
- [ ] User input properly validated
- [ ] No injection vulnerabilities

---

## Architecture Decision Records (ADRs)

yaah uses **Architecture Decision Records (ADRs)** to document significant architectural decisions.

See [docs/adr/README.md](docs/adr/README.md) for a complete list:

- [ADR-0001: Engine-View Separation](docs/adr/0001-engine-view-separation.md)
- [ADR-0002: Middleware Pipeline Pattern](docs/adr/0002-middleware-pipeline.md)
- [ADR-0003: Functional Options Pattern](docs/adr/0003-functional-options.md)
- [ADR-0004: Event-Driven Architecture](docs/adr/0004-event-driven-architecture.md)

---

## License

By submitting a PR, you agree your contribution is licensed under
`MIT OR Apache-2.0`, the same as the rest of the project.
