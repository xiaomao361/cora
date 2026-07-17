# Configuration

Cora has three configuration boundaries: Server runtime, Agent runtime, and
product-specific experience Packs. Keep them separate.

## Server

Copy `config/cora-server.example.yml` to a private deployment directory. The
important fields are:

- `server`: private listen address and port.
- `storage.path`: writable SQLite path on persistent storage.
- `core.experience_pack_dir`: optional private Pack directory. Omit it for
  observe-only behavior.
- `auth.bearer_token_file`: a mode-0600 token file outside Git.
- `aggregation`: flush cadence and active in-memory identity limit.

Validate after creating the referenced token and Pack directory:

```sh
cora-server -config.file=/etc/cora/server.yml -check-config
```

Unknown YAML fields are rejected. Relative paths resolve from the process working
directory, so production configuration should normally use absolute paths.

## Agent

Use `config/cora-agent.example.yml` for one log and
`config/cora-agent-multi.example.yml` for multiple logs. Replace:

- Server URL and bearer-token path.
- `product_line`, environment, and timezone.
- Each `app`, `node`, `deployment_group`, and `__path__` label.
- Positions path with a persistent, writable location.

Validate before starting:

```sh
cora-agent -config.file=/etc/cora-agent/agent.yml -check-config
```

Use one stable positions file per Agent process. Start new production installs at
`end` unless an intentional, bounded historical import has been planned.

## Experience Packs

`config/experience-packs/payments.example.json` is fictional and demonstrates the
schema only. A real Pack must live in a private repository or deployment store.
Every JSON file in the configured directory is loaded; duplicate product lines,
invalid decisions, and invalid trace roles fail startup.

Recommended private layout:

```text
/etc/cora/
  server.yml
  auth.token
  experience-packs/
    payments.json
```

No Pack means no hidden generic rules: unknown product lines receive the
framework-default `observe` decision. This is the safe public default.

## Model manifest

`config/cora-model.example.json` is used by the offline iteration workflow. Copy
it beside the private Pack, update the Pack path/version/SHA-256, and pass it
explicitly with `cora-iterate -pack-manifest ...`. The Server does not use this
manifest to discover Packs.

## Secrets and private facts

Never commit bearer tokens, real hostnames/IPs, log paths, business error text,
class names, Pack rules, production snapshots, or evaluation reports. Example IPs
and domains in the public repository use documentation-only values.
