# Sample `.slopbox.toml` configs

Drop one of these into a project as `.slopbox.toml` (commit it) and slopbox will:

- install the declared tools on startup (via `mise` — see the matching tool
  versions you'd put in the project's own `mise.toml`/`.tool-versions`), and
- show the `[[commands]]` as extra launcher buttons in the toolbar.

Settings precedence (highest first): env → `.slopbox.local.toml` → `.slopbox.toml`
→ defaults. Keep secrets/overrides in `.slopbox.local.toml` (gitignored).

| File | For |
|------|-----|
| [node.slopbox.toml](node.slopbox.toml) | Node/TypeScript web apps |
| [python.slopbox.toml](python.slopbox.toml) | Python services |
| [go.slopbox.toml](go.slopbox.toml) | Go services |
