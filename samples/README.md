# Sample `.shellraiser.toml` configs

Drop one of these into a project as `.shellraiser.toml` (commit it) and shellraiser will:

- install the declared tools on startup (via `mise` — see the matching tool
  versions you'd put in the project's own `mise.toml`/`.tool-versions`), and
- show the `[[commands]]` as extra launcher buttons in the toolbar.

Settings precedence (highest first): env → `.shellraiser.local.toml` → `.shellraiser.toml`
→ defaults. Keep secrets/overrides in `.shellraiser.local.toml` (gitignored).

| File | For |
|------|-----|
| [node.shellraiser.toml](node.shellraiser.toml) | Node/TypeScript web apps |
| [python.shellraiser.toml](python.shellraiser.toml) | Python services |
| [go.shellraiser.toml](go.shellraiser.toml) | Go services |
