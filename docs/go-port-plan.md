# Go port plan

Status: phases 0–5 done on the `go-port` branch; cutover (6) and incident
cleanup (7) remain. The bash `bin/incus-compose-update` is still the flake's
default package; the Go binary is `.#incus-compose-update-go`.

## Where it stands

- **Parity.** `check` output is byte-identical to the bash on the live stacks
  and on copies with stale pins (tag and digest modes, Docker Hub and ghcr.io,
  `order: pushed`, multi-page tag lists, the "no pin — skipping" path). The
  `apply-all` plan lists the same updates.
- **Real applies** ran against a throwaway Incus project: a digest-mode
  service with a volume and a tag-mode dependent, in dependency order, with
  snapshot, pin, recreate, re-ensure, identity check, commit and push; then a
  rollback by name. Snapshots were confirmed to ignore the volume's
  `snapshots.expiry`, and the stale-checkout guard refused as designed.
- **Tests** run in the Nix build (`go vet`, `go test ./...`), including real
  git repositories and an in-memory registry with Link pagination. Mutating
  the snapshot, identity and running→stopped checks each fails a test.
- **Not yet exercised:** an apply on the production stacks — none was pending.

## Deliberate differences from the bash

- `order: pushed` is an error on registries without push order (all but
  Docker Hub), instead of silently taking the lexically first tag.
- `order: version` lists every tag on Docker Hub too (paginated registry API),
  not only the 100 most recently updated.
- `check` reports a failing service and carries on, then exits non-zero.
- A missing registry repository is reported as "not found, or requires
  authentication".
- `tag_re` is Go RE2, validated when read, and required for `mode: tag`;
  `mode` and `order` values are validated.
- The "adopt what the server runs" suggestion keeps a digest's `sha256:`.
- Appending a new pin to a `versions.env` without a trailing newline starts a
  new line.
- Independent services in `apply-all` are ordered by render order (the bash
  used tsort's arbitrary order among them).
- Incus is reached through its API, on `$INCUS_REMOTE` or the CLI's default
  remote; the `incus` CLI is no longer a dependency. Registry requests use
  Docker credentials from `~/.docker/config.json` when present.
- New: `--dry-run` for `apply` and `apply-all`.

## Why

The script's size is not the reason. The reasons are failure modes that bash
makes easy and a typed language makes hard:

- **Silent failure.** `set -e` is suspended for the whole body of any function
  called as an `if`/`||`/`&&` operand. Both callers of `cmd_apply` do that, so
  every step in apply has to check its own status by hand, and one that
  doesn't is skipped silently — including the snapshot that makes a recreate
  reversible.
- **Global state as the calling convention.** `RENDERED`, `MODE`, `TAG_RE`,
  `PIN_VAR`, `HOST`, `REPO`, `TAG`, `WANT_ID`, `GOT_ID`, `MANAGED_DIRS` are set
  by one function and read by another, so which stack or service they
  describe at any point is implicit.
- **No tests.** The safety properties in the README are enforced by nothing
  but reading the code.
- **Hand-rolled registry client.** Token dance, `Link` pagination, Docker Hub
  vs OCI endpoints, manifest-list vs manifest digests, all in curl + jq — and
  it contains a known bug (see *Fix during the port*).

## Goals

- Behavioural parity for `check`, `check <svc>`, `apply <svc> <ref>`,
  `apply-all [--yes]`, `--help`, and every environment variable in the README.
- Same `x-update:` schema, same `versions.env` format, same commit messages.
- Every safety property in the README survives, each pinned by a test.
- One static binary from the same flake (`buildGoModule`).

## Non-goals

- **Not** reimplementing compose loading. The rendered `incus-compose config`
  output stays the source of truth, for the reason given at the top of the
  bash script: it is the same binary that deploys, so it cannot disagree with
  reality (overlays, interpolation, merge anchors).
- **Not** reimplementing `up`/`start`/`exec`. Those stay `incus-compose`
  invocations; their graph semantics are exactly what we must not re-derive.
- **Not** go-git. Git stays a subprocess: it must use the user's ssh config and
  agent, and needs real `rebase`.
- No new features during the port except those listed below as fixes.

## Layout

Module `github.com/OleMussmann/incus-compose-update`.

```
cmd/incus-compose-update/   main: flag parsing, dispatch, exit codes. The only
                            place that calls os.Exit.
internal/stack/             STACK_DIRS resolution; render (exec incus-compose
                            config, unwrap the 1.1.0 `project:` shape); typed
                            Service{Name, Image, DependsOn, XUpdate}; pin-var
                            extraction from the RAW compose.yaml; ManagedIn()
                            (replaces stacks_managing); DependencyOrder()
internal/imageref/          Parse(ref) -> {Host, Repo, Tag, Digest}; the
                            ported parse_image_ref. Own code, not ggcr's
                            name.ParseReference: ggcr normalises docker.io to
                            index.docker.io, while identity matching needs the
                            exact image.id / user.image_alias spellings
internal/registry/          Tags(host, repo), Digest(host, repo, tag) on
                            go-containerregistry (remote.List follows
                            pagination; remote.Head returns the index digest).
                            Docker Hub API client kept only for push-ordered
                            tag listing, which the registry API cannot give
internal/version/           tag filtering (regexp), a `sort -V`-compatible
                            comparator, newest selection, backwards guard
internal/pins/              versions.env read/write, comment-preserving,
                            atomic (write temp + rename)
internal/gitsync/           SyncIn / WarnIfStale / Commit / SyncOut; forces
                            GIT_TERMINAL_PROMPT=0 and ssh BatchMode
internal/incus/             interface Incus { Instances(project) []Instance{Name,
                            State}; ImageIdentity(project, inst); VolumeExists;
                            Snapshot(project, pool, vol, name, noExpiry) }
internal/compose/           interface Compose { Recreate; Start; Reensure;
                            Exec; Ps } over the incus-compose CLI
internal/apply/             the apply state machine and the apply-all planner
```

`internal/apply` depends only on interfaces, so the whole state machine runs in
tests against fakes.

### `incus` access: client library or CLI?

The Incus Go client (`github.com/lxc/incus/v6/client`) gives typed instance
state and config, and makes errors values instead of parsed stderr. The catch
is remote selection: the CLI honours the user's current remote. Plan: build
`internal/incus` on the client, loading the same config through the Incus
`cliconfig` package so the default remote matches. Spike this first (half a
day). If it fights back, ship a CLI-backed implementation of the same interface
and revisit — nothing above the interface changes.

## Error model

- Every step returns `error`. No step prints-and-continues except where the
  bash does so *deliberately*, and those become explicit:
  - recreate failure → recorded, verification decides;
  - git failures after a verified deploy → reported, never fatal;
  - `start` after recreate → failure ignored (no-op on the happy path).
- A typed `ApplyError{Step, Err, Rollback []string}` carries the rollback
  instructions currently echoed at each failure point, so apply-all can print
  "stopped at X — n of total applied" around them uniformly.
- `die` becomes returned errors; apply-all's "stop at first failure" is a loop
  `return`, not a subshell trick.

## Behaviour to preserve — each needs a test

From the README's safety list and the script's comments:

- Rendered config for metadata, raw file only for the pin var name
- 1.1.0 `project:` wrapper unwrapped
- Image ref parsing: digest, then tag, then host; ported registry host; `library/` for Docker Hub official images
- Digest mode resolves the ref's own tag, never `latest`
- OCI tag pagination followed to exhaustion
- Backwards guard; for `order: pushed`, compare only the dotted core
- apply-all resolves every ref before mutating anything
- Plan in dependency order; cycle falls back to render order with a warning
- Planned stack dir passed through; name lookup only across managed stacks; ambiguous name refused
- Snapshot names ≤ 64 chars; digests shortened
- Snapshot with `--no-expiry`
- Snapshot/pin failure aborts with nothing changed
- Pull/rebase before reading pins; refuse over a dirty `versions.env` when behind; abort a conflicting rebase
- Pin-vs-server check before snapshot; exception when applying what already runs
- `start` after recreate
- Stack-wide re-ensure bounded by `REENSURE_TIMEOUT`
- Nothing vanished **and** nothing running→stopped across an apply
- Verify by image identity (`image.id` or `user.image_alias`); version probe is liveness only
- Push once, retry once after rebase on rejection; never fatal
- Non-interactive apply-all refuses without `--yes`
- No-args means `check`; `--help` works without `STACK_DIRS`

## Fix during the port (not bug-for-bug)

1. **`order: pushed` is broken on every non-Docker-Hub registry.**
   `fetch_tags_oci` ends in `sort -u` and `newest_tag` then takes `head -1`,
   so "newest pushed" is the *lexically smallest* tag. The OCI tags API has no
   push times at all. Port: `order:
   pushed` is an error for any host without a push-ordered listing (today only
   Docker Hub), with a message pointing at `mode: digest`. Deliberately not
   back-ported to the bash: nothing configures it, runs are manual, and the
   guard would be obsolete at cutover.
2. **Docker Hub tag listing is one page of 100.** Fine for `order: pushed`
   (newest first), wrong for `order: version` on a busy repo. Paginate.
3. **A vanished registry repo** should say
   "repository not found on docker.io", not a bare 401/404.
4. **`check` is not isolated per stack** (noted in the bash): one unreachable
   registry stops the whole report. Collect per-service errors, print them,
   exit non-zero at the end.
5. **ERE → RE2.** `tag_re` moves from `grep -E` to Go `regexp`. Every value in
   the current stacks is RE2-compatible; backreferences
   would not be. Validate `tag_re` at parse time with a clear error.

## Testing

- **Unit tables**, one per row of the table above that is pure logic:
  imageref, snapshot names, `sort -V` comparator (include the
  `2026.8.22-9fea41204` vs `-777ba8fa4` case), backwards guard, dependency
  order (incl. cycle), pins round-trip preserving comments, managed-stack
  lookup where two stacks define the same service and only one manages it.
- **Registry** against `ggcr/pkg/registry` in `httptest`, with pagination and
  an index-vs-manifest digest case.
- **apply state machine** against fake `Incus`/`Compose`/`gitsync`: scripted
  scenarios for every failure branch — snapshot rejected, recreate non-zero,
  dependents vanished, dependents stopped, identity mismatch, no identity,
  probe silent, push rejected once, push rejected twice.
- **Golden output** for `check` and the apply-all plan, so the port can be
  diffed against the bash on real stacks.
- `go vet`, `staticcheck`, `go test` as flake checks (`nix flake check`).

## Phases

0. **Baseline.** Capture `check` output from the bash on all stacks as golden
   files. Decide the CLI library (stdlib `flag` is enough for four
   subcommands; `urfave/cli/v3` matches incus-compose if preferred).
1. **Scaffold.** Module, `buildGoModule` in the flake alongside the bash
   package (`incus-compose-update-go`), CI checks. Incus client spike.
2. **Pure packages** with their tests: imageref, version, pins, stack parsing,
   dependency order.
3. **`check`.** Registry package; run both implementations on the real stacks
   and diff. Parity here proves parsing, registry and selection.
4. **`apply`.** Add a `--dry-run` that prints the step list without mutating
   (useful permanently). First real apply on one low-risk, volume-less
   service; then one with a volume and a digest pin, to exercise the snapshot.
5. **`apply-all`.** Planner, confirmation, stop-at-first-failure.
6. **Cutover.** Flake's default package becomes the Go binary; bash kept as
   `incus-compose-update-legacy` for one release; README updated; then the
   bash and its runtime inputs (yq-go, jq, gawk, curl, gnused, gnugrep) are
   deleted.
7. **Strip incident history.** Code comments, READMEs and docs state facts
   and reasons, not past incidents ("hit live on …", "this used to …");
   incidents belong in the vault. The Go code is written that way from the
   start; at cutover, apply the same to the remaining remarks in the stack
   repos (Search_Providers `compose.yaml`/`README.md`, Buzz `compose.yaml`,
   `README.md`, `versions.env`).

Rough size: 1500–2000 lines of Go plus a similar amount of tests.

## Later

- `--json` output for check (for a dashboard or a notifier) — cheap once the
  results are structs, but out of scope until after cutover.
