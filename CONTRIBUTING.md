# Contributing to yaah

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

## License

By submitting a PR, you agree your contribution is licensed under
`MIT OR Apache-2.0`, the same as the rest of the project.
