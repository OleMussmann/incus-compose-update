# incus-compose-update

Generic incus-compose image updater. Reads per-service update metadata from
`compose.yaml` (`x-update:` blocks) and manages pins in a tracked
`versions.env` file.

## Use

```nix
# flake.nix
inputs.incus-compose-update.url = "github:OleMussmann/incus-compose-update";
# environment.systemPackages
[ incus-compose-update.packages.${system}.incus-compose-update ]
```

Or directly: `nix run github:OleMussmann/incus-compose-update -- --help`

`incus-compose` must be on `PATH`; the package brings its own `git` and `ssh`.
Incus is reached through its API, using the incus CLI's configuration: the
remote named by `$INCUS_REMOTE` (as incus-compose honours it), else the default
remote. Registry requests use Docker credentials from `~/.docker/config.json`
when present, and go anonymous otherwise.

## Commands

```
incus-compose-update check                  compare pinned tags against upstream, all services
incus-compose-update check <service>        same, scoped to one service
incus-compose-update apply <service> <ref>  snapshot, pin, recreate, re-ensure, verify
incus-compose-update apply-all [--yes]      apply every pending update, one at a time
```

No arguments means `check`. `check` reports a service it cannot check (an
unreachable registry, a `tag_re` that matches nothing), carries on with the
rest, and exits non-zero at the end.

`apply` and `apply-all` take `--dry-run`: every read-only step runs — the
registry lookup, the pin-versus-server check, the volume check — and the steps
that would change something are printed instead of run. A dry run does not
rebase the checkout; it only warns when it is behind.

### `apply-all`

Resolves every pending update across all stack dirs, prints the plan, asks once,
then runs `apply` for each in turn:

```
planned updates:

  stack: /srv/stacks/hermes
    hermes                 v2026.8.13 -> v2026.8.18

  stack: /srv/stacks/search
    api                    2.11.200 -> 2.11.212

recreate 2 service(s), one at a time? [y/N]
```

Every target ref is resolved *before* anything is mutated, so an unreachable
registry or a stale `tag_re` fails with all stacks still on their current pins,
rather than halfway through a run.

**Ordered dependencies-first.** Within each stack the plan is sorted
topologically on `depends_on`, so a service is never recreated before something
it depends on — recreating a dependency tears its dependents down, which would
undo an update applied to them earlier in the run. Services with no ordering
between them keep render order. A `depends_on` cycle falls back to render order
with a warning rather than skipping the stack.

`--yes` skips the prompt for unattended use. Without it, a non-interactive
invocation refuses rather than assuming consent.

**Sequential, never concurrent.** Services in a stack share one `versions.env`
that `apply` rewrites in place and commits, `up --recreate` starts linked
services (no `--no-deps`), and verification exec's into the container — all
three break under concurrency, while incus-compose already funnels image pulls
through one lock. Serial also means a failure has exactly one suspect.

**Every service is re-ensured, and nothing may disappear.** A service-scoped
`up --recreate` deletes the target's *dependents* in order to rebuild the
target, and does not bring them back — `up <svc>` ensures a service's
dependencies, never its dependents. So each `apply` follows its recreate with a
plain stack-wide `up -d` (no `--recreate`, no `--pull`), which rebuilds exactly
what was torn down and is a no-op for anything already running. It is bounded
by `REENSURE_TIMEOUT`, since that walk blocks on every `service_healthy` gate
in the stack.

As a backstop that does not depend on incus-compose's graph semantics, `apply`
brackets the recreate with the project's instance list and **fails if anything
present before the apply is missing after it, or was running before and is
stopped after it**, naming what was lost. Instances already stopped beforehand
are ignored. Ordering alone cannot cover this: a plan containing only a
dependency still tears its dependents down.

**Stops at the first failure.** The failed service prints its usual rollback
instructions; remaining services are untouched and still on their current pins.
Fix it, then re-run `apply-all` — already-applied services are up to date by
then and drop out of the new plan.

## Configuration

Environment variables:

| Var | Meaning | Default |
|---|---|---|
| `STACK_DIRS` | colon-separated stack directories | required (or `STACK_DIR`) |
| `STACK_DIR` | single-stack directory | alternative to `STACK_DIRS` |
| `INCUS_POOL` | storage pool | `local` |
| `INCUS_PROJECT` | Incus project | detected from `compose.yaml` |
| `AUTO_PUSH` | push each pin commit | `1` (set `0` to commit only) |
| `REENSURE_TIMEOUT` | dependency timeout for the post-recreate stack-wide `up` | `2m` |
| `READY_TRIES` | readiness probes after a recreate | `20` |
| `READY_INTERVAL` | seconds between readiness probes | `3` |
| `INCUS_REMOTE` | Incus remote | the incus CLI's default remote |

## The `x-update:` schema

Per-service metadata in `compose.yaml`. Absence means the service is unmanaged
— `check` skips it, `apply` refuses.

```yaml
services:
  my-service:
    image: registry/repo:${MY_SERVICE_TAG}
    x-update:
      mode: tag          # tag | digest (default: tag)
      tag_re: '^[0-9]+\.[0-9]+\.[0-9]+$'   # required for mode: tag
      order: version     # version | pushed (default: version)
      volume: none       # volume name | none (default: none)
      version_cmd:       # argv list (default: [<service>, --version])
        - my-service
        - --version
```

| Key | Values | Default | Meaning |
|---|---|---|---|
| `mode` | `tag` \| `digest` | `tag` | Pin a tag, or a digest of the tag in `image:` |
| `tag_re` | regular expression ([RE2](https://github.com/google/re2/wiki/Syntax)) | — | Required for `mode: tag`; filters upstream tags |
| `order` | `version` \| `pushed` | `version` | How to pick newest among matches |
| `volume` | volume name \| `none` | `none` | Volume to snapshot before recreate |
| `version_cmd` | argv list | `[<service>, --version]` | How to ask the running instance its version |

### Derived, never stated

- **Registry host and repo** — parsed from `image:`
- **Pin variable name** — read from the `${...}` in `image:`

### Modes

**Tag mode** (`mode: tag`): filters upstream tags by `tag_re`, selects newest
by version sort or push order, pins the tag string. Version sort orders tags
as GNU `sort -V` does. Push order needs a registry that reports it: the OCI
distribution API lists tag names only, so `order: pushed` works on Docker Hub
(through its own API) and is refused elsewhere — use `mode: digest` there.

**Digest mode** (`mode: digest`): fetches the digest of **the tag named in
`image:`**, pins the digest string. Use for images that publish no useful
versioned tags, or a rolling tag you want held still. The image reference
keeps its tag (`repo:13-slim@sha256:...`) so the human-readable label
survives — and the tag is what gets resolved, so the label and the digest
always describe the same image.

## Pin file

`versions.env` (tracked) holds version pins. The tool is the only writer.
Secrets stay in `.env` (gitignored).

```
# versions.env
MY_SERVICE_TAG=1.2.3
OTHER_TAG=4.5.6
```

`incus-compose` must receive both files:
```
incus-compose --env-file .env --env-file versions.env up -d
```

Or set `INCUS_COMPOSE_ENV_FILE='.env,versions.env'` in your shell.

## Several checkouts, one server

`versions.env` is a *record of what one server runs*, and more than one
checkout usually describes that same server — two workstations that are both
just interfaces to the same Incus host. Freshness is therefore a correctness
property, not tidiness. A stale checkout:

- misreports the running version (`check` reads its pin, not the server),
- writes a `from` version into its commit message that was never deployed,
- and can roll the server **backwards** on a plain `incus-compose up`, which
  interpolates image refs from whatever `versions.env` is in that tree.

So a run:

1. **pulls first.** Each stack dir is fetched and rebased onto its upstream
   before any pin is read — during `apply-all`'s planning phase, so the plan
   is built from pins that match the server. `check` only warns, since asking
   what is out of date should not rewrite your tree.
2. **checks the pin against the server.** Before the snapshot, the pin being
   moved is compared to the instance's own `image.id` / `user.image_alias`.
   If they disagree, this checkout does not describe the server and the run
   stops with nothing touched. The exception is applying the ref the instance
   *already* runs — that moves the record towards reality, and is the rollback
   printed when a recreate fails to take.
3. **pushes the commit.** Set `AUTO_PUSH=0` to commit without pushing.

Git failures are reported but never fatal to a verified deploy: by the time
the push runs, the container is already up and its image identity confirmed,
so a rejected push means the record lagged, not that the update failed. A push
rejected by a concurrent update from the other checkout is retried once after
a rebase. Network git is forced non-interactive (`GIT_TERMINAL_PROMPT=0`,
ssh `BatchMode=yes`) so a credential prompt fails fast instead of hanging
mid-run.

Two cases stop the run rather than guess, because only a human knows which
side is right:

- **both checkouts moved the same pin** — the rebase conflicts, is aborted,
  and the tree is left untouched. The server is the tiebreaker: whatever it
  runs now is the truth.
- **`versions.env` is dirty and the tree is behind** — almost always a pin
  written by an `apply` that then failed before committing. Rebasing over it
  would either stash a pin that is actually deployed or conflict on the very
  line about to be rewritten.

## Safety

- **Backwards guard**: errors if computed newest is older than current pin.
  For `order: pushed`, only the part before the first `-` is compared: tags
  like `2026.8.28-a30b2d474` carry an opaque build suffix that version sort
  would read as a number
- **Snapshot before recreate**: configurable per-service via `volume`. The
  snapshot never expires, whatever the volume's `snapshots.expiry`, so a
  rollback point outlives scheduled snapshot retention. Its name is
  `pre-<ref>-<YYYYmmddHHMM>`, with a digest shortened to `sha256-<12 hex>` to
  stay within Incus's 64-character limit. If the snapshot fails, nothing is
  changed
- **Verify after recreate**: checks the instance's own record of the image it
  was built from (`image.id` / `user.image_alias`) against the new pin, then
  that the container answers `version_cmd` at all. Image identity is the fact
  that settles whether the recreate took; the version string is only a
  liveness probe, and is allowed to be unchanged — two rebuilds of a
  digest-pinned base image can report the same release
- **Pull before, push after**: see [Several checkouts, one
  server](#several-checkouts-one-server)
- **Refuses to apply from a stale checkout**: if the pin being moved is not
  what the instance actually runs, the run stops before the snapshot
- **OCI pagination**: follows `Link` headers to exhaustion on ghcr.io and
  other OCI registries
- **Digest mode follows the ref's tag**, never a hardcoded `latest` — a pin
  can't drift onto a different image than the one `image:` names
- **Plan before mutate**: `apply-all` resolves every target ref up front and
  aborts the whole run if any of them cannot be resolved
- **Confirm before recreate**: `apply-all` prompts once, and refuses to run
  non-interactively unless given `--yes`
- **Start after recreate**: a service-scoped `up` can build the instance
  correctly and still leave it stopped, because its "Starting resources" phase
  bails on any resource the run did not ensure — including sibling services'
  images. `apply` follows every recreate with a `start`, which is a no-op when
  the service is already running
- **Shared service names**: a service name only identifies a service within
  one stack. `apply <service>` uses the stack in which that service has an
  `x-update:` block, and refuses a name managed in more than one stack
  (narrow `STACK_DIRS` to pick one)

## Development

```
cmd/incus-compose-update/  flags, environment, dispatch
internal/stack/            rendered config, x-update:, pin variable, dependency order
internal/imageref/         image reference parsing
internal/version/          sort -V ordering, newest-tag selection, backwards guard
internal/pins/             versions.env read/write
internal/registry/         tags and digests (go-containerregistry, Docker Hub API)
internal/incus/            instances, volumes, snapshots (Incus client library)
internal/compose/          incus-compose invocations
internal/gitsync/          pull, commit, push
internal/apply/            check, apply, apply-all
```

`nix build` runs `go vet` and `go test ./...`. Locally:
`nix shell nixpkgs#go --command sh -c 'CGO_ENABLED=0 go test ./...'`.
