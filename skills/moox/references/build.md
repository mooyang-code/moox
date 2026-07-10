# Build

Run root-level build and boundary checks from the monorepo root:

```bash
make build
make check-boundaries
```

`make build` writes binaries to `bin/`:

- `moox-cli`
- `moox-admin`
- `moox-web-host`
- `moox-cloudnode`
- `moox-storage`
- `moox-collector`
- `moox-collector-scf`
- `moox-eventbus`
- `moox-host-agent` (Linux amd64/arm64 builds only)
- `moox-host-agent-cli` (Linux amd64/arm64 builds only)
- `moox-factor`
- `moox-trade`

The default `moox-storage` binary builds the full storage service, including Access, PrimaryStore, view building, text indexing, and archive services.

Pebble is used for the online ordered PrimaryStore and does not require an external C++ KV library. DuckDB still uses the module's normal CGO-enabled build path.

```bash
make build
```
