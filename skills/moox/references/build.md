# Build And Test

Run all root-level validation from the monorepo root:

```bash
make test
make build
make acceptance
```

`make build` writes binaries to `bin/`:

- `moox-cli`
- `moox-server`
- `moox-storage`
- `moox-collector`
- `moox-factor`
- `moox-order`
- `moox-account`

The default `moox-storage` binary builds the full storage service, including Access, PrimaryStore, view building, text indexing, and archive services.

Pebble is used for the online ordered PrimaryStore and does not require an external C++ KV library. DuckDB still uses the module's normal CGO-enabled build path.

```bash
make build
```
