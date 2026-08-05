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
```

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
| `mode` | `tag` \| `digest` | `tag` | Pin a tag, or a digest for `latest`-only images |
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

**Digest mode** (`mode: digest`): fetches the digest of `latest`, pins the
digest string. Use for images that only publish `latest` (no versioned tags).
The image reference stays `repo:latest@sha256:...` so the human-readable label
survives.

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
- **Verify after recreate**: compares running version before/after
- **Commit but never push**: pins are committed locally, pushed manually
- **OCI pagination**: follows `Link` headers to exhaustion on ghcr.io and
  other OCI registries
