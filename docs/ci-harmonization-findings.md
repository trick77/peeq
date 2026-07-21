# CI/release harmonization across the Go + React/Vite repos

Completed 2026-07-21. Reference implementation: **peeq** (this repo).
In scope: **loom, music, lens, lens-console**. Out of scope: trift (findings
recorded in trick77/trift#52), vibecheck.

This document was rewritten at the end to state what is true now. An earlier
draft accumulated corrections on top of stale tables — precisely the failure
mode described under "Read the right files" below.

## Final state

| repo | PR(s) | backend lines | UI lines before → after |
|---|---|---|---|
| peeq | #43, #44, #48 | 80.7% | 78.2% |
| loom | #493, #499 | 76.3% | 77.8% |
| music | #219, #220 | 84.4% | **52.5% → 85.9%** |
| lens | #596, #598 | 78.8% | **44.1% → 77.1%** |
| lens-console | #69, #72 | 76.8% | **0% → 98.4%** |

All five now have:
- `.github/workflows/ci.yaml` (no `test.yaml` anywhere), triggered on
  `pull_request` + `workflow_dispatch`, with `permissions:` and `concurrency:`
- job names `Backend (build + test)` / `UI (build + test)`
- `go build ./...`, `go vet ./...`, `-race` with `CGO_ENABLED=1`, `-coverpkg=./...`
- `hack/coverage-gate.sh` — absolute floor on **line** coverage via a Cobertura
  conversion, with `hack/coverage-floors` at `backend=75.0` / `ui=75.0`. One
  definition of the floor, nowhere else.
- `hack/patch-coverage.sh` — 80% on changed lines. **Byte-identical in all five**,
  md5 `4d9dc015a487796201bf4839f7c5d602`.
- `hack/coverage-gate.test.sh` self-tests
- vitest `coverage.include`, `json-summary` + `lcov` reporters,
  `reportsDirectory: ../coverage/ui`
- release `paths-ignore`, tag created only after the image push, image cleanup
  with a package-existence guard

## The bugs this uncovered

### 1. The patch gate never worked (absolute `<sources>`)
`gocover-cobertura` writes an **absolute** path into `<sources>`. diff-cover
joins it with each module-relative filename, producing paths that never match
git's — so every file silently missed and the gate passed vacuously at "No lines
with coverage information". `--src-roots` does **not** override this; the
embedded `<sources>` wins. Rewriting the element to a repo-relative path is the
fix.

loom had shipped this for months. Its PR #492 was red because of it, unrelated
to that PR's own changes. Only loom's `assert_matched` guard made it visible —
without it the gate would have passed vacuously forever.

### 2. False failure when a diff has no executable lines
The guard could not distinguish "the paths are broken" from "the changed lines
are not instrumented" — a Go string inside a package-level `var catalog =
[]struct{...}`, a type-only TS change, a struct tag. Fixed by asking whether the
changed files appear in the report **at all**, rather than whether their lines
matched. (peeq#44 / loom#492.)

### 3. Setup files trip the same guard
Vitest setup files are the `setupFiles` entry, excluded from coverage, so they
never appear in the report. music and lens-console each hit this and fixed it
*differently* — music filtered its own filename, lens-console moved the file out
of `src/`. Two fixes for one problem, in a file meant to be identical. Resolved
in peeq#48 by filtering every convention in use (`src/test-setup.ts`,
`vitest.setup.ts`, `src/test/setup.ts`, `test-setup.ts`) plus `.d.ts`.

### 4. `coverage.include` was hiding untested files
Without it, vitest's v8 provider only instruments files that a test imports —
wholly untested files vanish from **both** sides of the ratio and inflate the
percentage. peeq reported 31 of 37 UI files before `include` was set. lens
measured 44.1%, not the 56% a naive run suggested.

### 5. `account: user` silently no-ops on org repos
`ipverse` is an **Organization**, so `account: user` in
`snok/container-retention-policy` matches nothing and the cleanup does nothing.
lens-console now uses `${{ github.repository_owner }}` and an `orgs/` probe.
`trick77` is a User account, so peeq/loom/music are correct as-is.

### 6. music was measuring only its logic layer
`ui/vite.config.ts` excluded `src/**/*.tsx` wholesale, justified by a comment
claiming components were covered by a Playwright suite. No Playwright config
existed in the repo. It reported 98.15%; the honest figure with `.tsx` included
was 52.46%. Now 85.9% against the same 2371-line denominator.

## Read the right files

Two incidents, one root cause — **stale or wrong sources**:

- A survey agent reported loom had no release `paths-ignore` and kept 5 images.
  Both false; loom had had them since #491. It had read stale copies under
  `loom/.claude/worktrees/`.
- **Every** local master was behind origin, and origin already contained release
  `paths-ignore` and/or cleanup work reported as missing: loom #491, music #218,
  lens #595, lens-console #68. This caused a conflicting PR in both lens-console
  and music.

Always `git fetch` and branch from `origin/master`. Never survey a local
checkout without fetching first, and never trust a scan that may have hit
`.claude/worktrees/`.

## Deliberate divergences — do not "fix" these

- **lens `release.yaml`** versions from `ui/package.json` with a `v` prefix
  across 463 tags. The family's tag glob `[0-9]*.[0-9]*.[0-9]*` would not match
  `v0.14.137`, so a naive switch silently restarts versioning at 0.0.1. Left
  untouched; needs a deliberate migration.
- **lens `cleanup-images.yaml`** is hand-rolled `gh api` on purpose: it avoids
  the deprecated node20 runtime *and* prunes stale git tags, neither of which
  the retention action does.
- **lens keeps staticcheck** rather than golangci-lint until the lint pass.
- **music's `music-align` image** is deliberately not in its cleanup
  `image-names`. Deferred.

## Worth copying outward

- **loom's `compose_test.go` asserts on its own workflow files.** It caught a
  workflow rename immediately. No other repo does this.
- **loom's "Fetch base ref" step** — a `pull_request` build checks out the merge
  ref, so `refs/remotes/origin/<base>` is not guaranteed to exist even with
  `fetch-depth: 0`. Now adopted everywhere.
- **`npm run build` in CI** — vitest runs neither tsc nor vite build. Added to
  lens, it immediately caught a real type error (`easter_egg` is
  `string | null`, asserted as `true`).

## Still open

- **lens release migration** — the `v`-prefix problem above.
- **Linting pass** — golangci-lint (bundles staticcheck, replacing lens's
  standalone step), eslint, prettier `format:check`. Deferred deliberately so a
  lint backlog could not block reaching 75%. Prettier means a one-time
  mechanical reformat in peeq, loom and music.
- **lens-console backend has under 2pp of headroom** (76.8% vs a 75.0 floor).
- **Copy loom's workflow-assertion tests** to the other four repos.
- **lens-console dependabot PRs** #64–#67 are open; #64 (actions v6→v7) is now
  redundant.
