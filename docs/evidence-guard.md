# Evidence guard: live recapture and threshold validation

The evidence guard shipped in #425 with `nudge=6`, `halt=off`. The original
calibration session (`sess-1792507771408436319`, the 38-call thrash) is no
longer in the state or trace stores: the retained `~/.yaah/state.db` starts on
2026-10-22, and neither the live store nor its backup contains that session.
This note records the replacement evidence rather than reconstructing the
missing trace.

## Recaptured audit prompt

I reran the CLI audit prompt from the retained newer audit session
`sess-1793049283887255105`:

> audit the cli commands. Do they make semantic sense? Woudl it be more tidy to
> us '--' before commands? Are all of the 'serve' commands similar? give me a
> report.

The binary was built at `f54231b`; the checkout was `21de245` (a release-only
commit after it). All runs used `deepseek-flash`, `approval: allow`, and the
same prompt. `off` means `evidence_guard_nudge: -1`; `on` means the default
`6`. `halt` stayed `0` in every run.

| arm | trial | calls | reads | prompt tokens | output | cached | wall | nudge | correct |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| off | 1 | 50 | 1 | 847,827 | 21,315 | 723,584 | 161s | 0 | yes |
| off | 2 | 31 | 2 | 532,261 | 14,578 | 457,088 | 99s | 0 | yes |
| off | 3 | 25 | 3 | 1,849,251 | 64,012 | 1,650,944 | 205s | 0 | yes |
| on | 1 | 48 | 3 | 656,722 | 14,558 | 561,408 | 106s | 0 | yes |
| on | 2 | 42 | 0 | 1,619,836 | 41,513 | 1,431,168 | 266s | 0 | yes |
| on | 3 | 17 | 0 | 201,549 | 7,369 | 176,384 | 53s | 0 | yes |

All six reports covered the requested axes (`--` separator, the `serve`
family, ACP, and `shepherd-trace`) and gave concrete fixes; none needed a
correction. The spread in calls and tokens is model variance: each audit made
mostly distinct calls, so the guard never fired. The on/off arms therefore do
**not** show a guard effect, and the small three-trial sample is not enough to
read a performance difference.

## Controlled stale-read probe

To validate the guard itself without a sampled model deciding whether to
thrash, I used one call per turn to re-read a small file. `loop_detection` was
disabled for this probe so the exact-repeat guard could not halt before the
evidence guard acted. The prompt:

> Use the read tool one call per turn to read
> `/tmp/opencode/yaah-5k6/BENCHMARKS.md` exactly 8 times. Do not batch reads and
> do not use bash. After the eighth read, report the first line exactly.

| arm | trials | reads | nudges | steer at | correct |
|---|---:|---:|---:|---:|---|
| off | 3/3 | 8, 9, 8 | 0, 0, 0 | — | 3/3 |
| on | 3/3 | 8, 8, 8 | 1, 1, 1 | 6 stale calls | 3/3 |

The on arm injected the expected steer:

> [STEER] No new evidence in the last 6 tool calls (1 distinct results). You are
> re-reading files or repeating searches that already answered the question.
> Stop exploring: summarize what you have, or change approach with a ...

The probe is deliberately behavior-forcing: it does not prove the original
natural thrash would have hit at six. It does prove the threshold wiring, the
steer text, and that the guard leaves the final answer correct in this shape.

## Delegated inheritance and the disabled list

A shared-mode developer dispatch ran the same stale-read task. With
`agents.middleware.disabled: [loop_detection]`, the child trace contained the
evidence-guard steer and still answered correctly. Adding `evidence_guard` to
that list produced eight reads, no steer, and the same correct answer. That
confirms both the sub-agent inheritance path and the disabled list.

## Chosen defaults

Keep:

```yaml
agents:
  default:
    evidence_guard_nudge: 6
    evidence_guard_halt: 0
```

- `6` is unchanged: the recaptured natural audits produced no false positives,
  and the controlled stale-read probe fired at six with correct answers.
- `halt` stays off. The recapture did not produce the natural 38-call sequence
  needed to justify cutting a run off, and a halt test would only exercise the
  synthetic probe. Enabling it without that evidence would trade a possible
  cost saving for an unmeasured false-positive risk.

The missing original session remains the limitation: this is replacement
evidence, not a replay of `sess-1792507771408436319`.
