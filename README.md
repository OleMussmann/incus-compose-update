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

## Commands

```
incus-compose-update check                  compare pinned tags against upstream, all services
incus-compose-update check <service>        same, scoped to one service
incus-compose-update apply <service> <ref>  snapshot, pin, recreate, verify
incus-compose-update apply-all [--yes]      apply every pending update, one at a time
```

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

`--yes` skips the prompt for unattended use. Without it, a non-interactive
invocation refuses rather than assuming consent.

**Sequential, never concurrent.** Services in a stack share one `versions.env`
that `apply` rewrites in place and commits, `up --recreate` starts linked
services (no `--no-deps`), and verification exec's into the container — all
three break under concurrency, while incus-compose already funnels image pulls
through one lock. Serial also means a failure has exactly one suspect.

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
| `tag_re` | ERE string | — | Required for `mode: tag`; filters upstream tags |
| `order` | `version` \| `pushed` | `version` | How to pick newest among matches |
| `volume` | volume name \| `none` | `none` | Volume to snapshot before recreate |
| `version_cmd` | argv list | `[<service>, --version]` | How to ask the running instance its version |

### Derived, never stated

- **Registry host and repo** — parsed from `image:`
- **Pin variable name** — read from the `${...}` in `image:`

### Modes

**Tag mode** (`mode: tag`): filters upstream tags by `tag_re`, selects newest
by version sort or push order, pins the tag string.

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

## Safety

- **Backwards guard**: errors if computed newest is older than current pin
- **Snapshot before recreate**: configurable per-service via `volume`
- **Verify after recreate**: checks the instance's own record of the image it
  was built from (`image.id` / `user.image_alias`) against the new pin, then
  that the container answers `version_cmd` at all. Image identity is the fact
  that settles whether the recreate took; the version string is only a
  liveness probe, and is allowed to be unchanged — two rebuilds of a
  digest-pinned base image report the same release (`debian:13-slim` is
  `13.6` before and after), which the old before/after comparison mistook for
  a failed update, and then mistook again on the rollback it suggested
- **Commit but never push**: pins are committed locally, pushed manually
- **OCI pagination**: follows `Link` headers to exhaustion on ghcr.io and
  other OCI registries
- **Digest mode follows the ref's tag**, never a hardcoded `latest` — a pin
  can't drift onto a different image than the one `image:` names
- **Plan before mutate**: `apply-all` resolves every target ref up front and
  aborts the whole run if any of them cannot be resolved
- **Confirm before recreate**: `apply-all` prompts once, and refuses to run
  non-interactively unless given `--yes`
- **Start after recreate**: a service-scoped `up` under incus-compose 1.2.0 can
  build the instance correctly and still leave it stopped, because its
  "Starting resources" phase bails on any resource the run did not ensure —
  including sibling services' images. `apply` follows every recreate with a
  `start`, which is a no-op when the service is already running
