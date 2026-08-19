# AGENTS.md

Read this before touching the repository. `PLAN.md` holds the architecture and the verified
upstream constraints; this file holds how we work.

## What this project is

A single Go binary that runs on a streamer's PC, takes a WHIP ingest from a phone, and hands OBS
a WHEP link. Two links, one delay slider, one stats row. Everything else is out of scope until
that works and is boring.

## Minimalism is the product

The reason to choose this over BELABOX or a cloud ingest is that it is small enough to read in an
afternoon. Every addition spends that budget.

- Keep it small enough to read in an afternoon. There is no line limit, because a number that gets
  revised whenever it is inconvenient is paperwork rather than a constraint. The discipline is not
  relaxed by dropping it; it moves into the rules below, which are the things that actually keep a
  codebase small. Report the production count in any PR that moves it more than 200 lines, so
  growth stays visible.
- Every phase pays for its lines in capability. Adding a lot of code and no new behavior is the
  signal to re-read that code. Growth that buys something is fine at any size.
- Before writing a new function, look for the one that already does it. This rule kept being
  broken while it was only a rule, so it is now checked two ways, and both run before a commit:
  `golangci-lint` with `dupl` at 40 tokens catches near-copies with renamed variables, and
  `scripts/dupes.py` catches byte-identical bodies that `dupl` structurally cannot see, because it
  analyses one package at a time for one GOOS. Between them they cover the two mistakes actually
  made here: the SDP exchange copied into a second package, and `run` copied into a build-tagged
  sibling file. Neither catches a near-copy across packages; that one still needs reading.
- Delete on the way past. An export with no caller, a field that is never read, a counter that is
  never incremented, a helper that only forwards: remove it in the change that revealed it rather
  than filing it.
- Optimization is not a later phase. The relay touches every packet, so per-packet work and
  allocation rate are design constraints, not tuning: no payload parsing outside the one keyframe
  function, no allocation in a read loop that a pooled buffer avoids, no polling where a signal
  works, and genuinely idle when nothing is streaming. Claims still need a benchmark or a pprof
  profile in the PR; what does not need proof is the choice to keep the hot path short.
- Pion, a tray library, and `testify` in tests are the entire dependency budget. It has already
  been spent. Adding a fourth needs a line in the PR saying what it replaced and why writing it
  ourselves was worse.
- Media dependencies specifically are closed. `gortsplib`, `gosrt`, an RTMP server, an MPEG-TS
  muxer, and an AAC encoder were each evaluated and rejected with reasons recorded in `PLAN.md`.
  Do not reintroduce one without reading that table first; the usual trigger is wanting HEVC in a
  Browser Source, and that is a Chromium version lag that resolves itself.
- No framework in `web/`. Plain HTML, one CSS file, one JS file with event listeners.
- No configuration for something with one sane value. A setting is a support burden forever.
- Ingests are addressed by stream key on the shared UDP media port. Never allocate a port per
  ingest — that turns "forward two ports" into "forward one per camera" and breaks the setup story
  the product is built around.
- The 2000 ms delay floor is not a default, it is a floor. Do not add a flag, an "advanced" toggle,
  or an env var that lets someone go below it. Clamp on config load rather than trusting the file.
- No abstraction with one implementation. No interface until there are two callers. The one
  standing exception is `internal/autostart`, which is genuinely three implementations of one
  contract, one file per platform.
- Keep cgo out of every package except `internal/tray`. A cgo-free core is what makes the static
  Linux build and the headless default work, and it is easy to lose by accident.
- Delete on sight: unused exports, defensive branches for states that cannot occur, wrappers that
  only forward.

## Code style: follow pion

The Go here should read like it came out of the `pion/webrtc` repository. Someone who reviews Pion
should be able to review this without adjusting. `.golangci.yml` is Pion's own config copied
verbatim — only the SPDX header differs — so the linter enforces most of this. Run it and believe
it; do not add `//nolint` to get past a rule Pion lives with.

The rules that bite hardest, because they are not what an LLM writes by default:

- **SPDX header on every file**, Go and YAML alike, as the first two lines:
  `// SPDX-FileCopyrightText: 2026 wagaStrim contributors` then
  `// SPDX-License-Identifier: MIT`. Enforced by `goheader`. Carry a `.reuse/dep5` and
  `LICENSES/MIT.txt` the way Pion does.
- **Sentinel errors only.** `err113` forbids `errors.New` and bare `fmt.Errorf` at a call site.
  Declare every error once in a package-level `var` block in `errors.go`, named `ErrThing`
  (`errname`), message lowercase and unpunctuated. At the call site wrap it:
  `fmt.Errorf("%w: parsing offer: %w", ErrBadOffer, err)`.
- **No `fmt.Print*`, `log.Print/Fatal/Panic`, `os.Exit`, or bare `panic`.** `forbidigo` blocks all
  of them outside `cmd/`. Return an error and log through `pion/logging` so our output matches
  Pion's. This is the single rule most likely to be violated by reflex.
- **No package-level globals** except sentinel errors (`gochecknoglobals`).
- **`any`, never `interface{}`** (`revive use-any`).
- **Comments end in a period** (`godot`). Every package gets a package comment
  (`revive package-comments`).
- **Blank line before `return`, `break`, `continue`** (`nlreturn`). Pion's source does this
  everywhere; matching it is most of what makes code look like theirs.
- **Variable names at least 2 characters** and short names only for short scopes
  (`varnamelen`, max-distance 12). Receivers and the listed `i`/`n`/`w`/`r`/`b` are exempt.
- **Complexity is capped** by `cyclop`, `gocognit`, `gocyclo`, `nestif`, and `maintidx`. `funlen`
  is disabled, so a long straight-line function is fine and a branchy short one is not. That is
  the right trade for this project.
- **`dupl` is on.** Two near-identical blocks fail the build, which is the honest pressure to
  factor rather than paste.
- **Tests use `testify/assert`,** not `t.Error`/`t.Fatal` — `forbidigo` blocks those with that
  exact message. This is the one dependency exempt from the justify-it rule above.
- **Formatting is `gci`, `gofmt`, `gofumpt`, `goimports`.** `gofumpt` is stricter than `gofmt`;
  run all four.

Beyond the linter, write for someone reading the file cold at 2am during a broken stream.

- Prefer one obvious function over three clever ones. Straight-line code beats indirection.
- Name for the domain: `whipSession`, `playoutBuffer`, `streamKey`. Not `Manager`, `Handler`,
  `Service`, `Helper`, `Util`.
- Comments explain *why*, never *what*. A comment restating the line below it gets deleted. Where
  a comment earns its place is the non-obvious ones: why the drift correction skips to a keyframe
  instead of resampling, why renomination is set on the controlling agent, why FEC is absent.
- Errors say what failed and with what input, wrapping a sentinel as described above. Never a
  message like "failed" or "error occurred".
- Handle every error at the point it happens. No bare `_ =` on anything that can fail in
  production.
- Standard library first. `net/http` and `html/template` are enough for this UI.

## Anti-slop

Output that reads as machine-generated gets rejected regardless of whether it works.

- No banned filler in comments, docs, commits, or PR text: "robust", "seamless", "comprehensive",
  "leverage", "delve", "it's worth noting", "in today's fast-paced".
- No emoji in code, commits, or PR bodies. The UI has one status dot; that is the visual budget.
- No section headers with nothing under them. No tables with one row. No bullet list where a
  sentence works.
- No summary paragraph restating what the diff already says.
- When something can only fail in one of two obvious ways, name which one happened and what to do
  about it. "This is the receiver link, OBS needs the sender link" beats a correct-but-useless 403.
- Never let a user pick a combination that cannot work and find out from a black screen. A codec
  the chosen receiver path cannot decode must be flagged where it is chosen, not diagnosed later.
- Do not claim a feature exists before it does. Specifically: the bonded receive path works, but
  nothing in the UI or README says "bonded" until a sender exists that actually sprays over
  multiple candidates. Until then the word is failover.
- No speculative generality — no config knob, hook, or extension point added "for later".
- Run `/slop-check` on the diff before opening a PR.

## Review before push

Never push straight from generation.

1. Re-read the full diff yourself, top to bottom, as a reviewer and not as the author.
2. Run `gofumpt -l .`, `go vet ./...`, `golangci-lint run`, `scripts/dupes.py`, and
   `go test -race ./...`. All clean, no exceptions, no `//nolint` added to make it so.
3. Run `/code-review` on the diff. Fix or explicitly justify every finding.
4. Run `/slop-check`.
5. Only then commit.

If a change touches media handling, say in the PR how it was verified. "It compiles" is not
verification for this project. The automated sender/receiver harness is the floor; anything
touching the buffer, NACK sizing, or the bonded receive path needs the impairment matrix from
`PLAN.md`, and anything touching SDP or codec negotiation needs a real phone.

Performance claims need evidence. A PR that says a change is faster carries either a
`go test -bench` delta or a pprof profile. Without one it is a guess, and guesses get reverted
later by someone who cannot tell why the code is shaped that way.

## Commits and PRs

There is no GitHub remote yet and there will not be one until the project is done. Everything
below still applies to local history — a messy local log becomes a messy public log the moment the
repo is created, and nobody rewrites it at that point.

We are deliberately slow. Repository noise is the failure mode here, not slow delivery.

- **The daily cap is lifted while working through the phases in `PLAN.md`.** One commit per phase
  is still the shape; that is what keeps history readable. It is not licence to commit per file or
  per fix. Corrections to a phase still on its own unpushed branch get amended into it rather than
  stacked, since nobody has pulled it.
- Outside phase work, five commits per day maximum. If the sixth seems necessary, squash instead.
- One branch per phase from `PLAN.md`, and it only merges once that phase actually runs. These
  become the PRs retroactively if the history is worth preserving at repo creation.
- No commit that only touches formatting, comments, or docs unless that is the entire point of the
  PR. Fold it into the change that motivated it.
- No "wip", "fix", "update", or "address feedback" messages. Every message names the behaviour
  that changed.
- Conventional prefixes: `feat:`, `fix:`, `perf:`, `refactor:`, `docs:`, `chore:`.
- PR bodies: what changed, why, how it was verified. Three short paragraphs at most, no template,
  no checklist theatre, no generated summary.
- Never amend or force-push a branch someone else has pulled.

## Nothing about Claude reaches GitHub

The repository is going public. It must look like it was written by a person.

- `.gitignore` carries `CLAUDE.md`, `.claude/`, `.serena/`, and `*.local.md`. It is already in
  place, so nothing assistant-related ever enters history in the first place — this is far easier
  than scrubbing it later. Verify at repo creation and again before flipping public.
- No `Co-Authored-By: Claude` trailers, no "Generated with Claude Code" footers, no assistant
  attribution anywhere in commits, PRs, issues, or code comments.
- No `.claude` directory committed, ever, including workflow or settings files.
- Before repo creation: `git log --all -p | grep -iE 'claude|serena'` must return nothing, not
  just `git ls-files`. A file that was committed and later ignored still sits in history.

## UI

Reuse the design language from `Anyways-BotGateway` rather than inventing one.

- Tokens: `--bg #faf9f5`, `--panel #fffefb`, `--line #e4e1d9`, `--text #141413`,
  `--muted #63615c`, `--accent #c8477e`, `--accent-text #a83263`, `--accent-soft #fbe7f0`,
  `--radius 5px`. Dark mode via `prefers-color-scheme` with the same names.
- Reuse the class shapes: `.button` for the soft pink action button, `.secret` for the monospace
  sunken block holding a masked link, `.stat` for the stats row, `.status-button` with
  `.good`/`.medium`/`.bad` for the live indicator, `.row` for horizontal groups.
- Both links render masked with an eye toggle and a copy button. A link is never logged, never
  screenshotted into docs, and never included in a stats payload.
- System font stack. No web fonts, no icon library, no CSS framework.

## Security

- Keys are 32 hex characters from `crypto/rand`, comparable only with
  `crypto/subtle.ConstantTimeCompare`, and regenerable from the UI.
- Sender and receiver keys are separate, always. Never collapse them back into one shared key for
  an ingest, however convenient it looks — the receiver link is meant to be handed to other people,
  and a shared key would let anyone holding it publish over the broadcast.
- An error message may only be more specific than "not found" for a caller already holding a valid
  key for that ingest. Everything else gets a generic 404, so nothing becomes an oracle for which
  ingests exist.
- Bind loopback by default. Exposing to LAN or the internet is an explicit opt-in that shows what
  it means in plain words first.
- Never log a stream key, a full ingest link, or a TURN credential — not at debug level either.
- Config file written `0600` through a temp file and rename.
- Dependency updates land in their own PR, never folded into a feature.
