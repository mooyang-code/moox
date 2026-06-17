## ADDED Requirements

### Requirement: Canonical module names
The repository SHALL use canonical module directories for the major quant-data platform responsibilities.

#### Scenario: First-stage module set
- **WHEN** the first-stage module layout is created
- **THEN** `modules/` SHALL contain or reserve `control`, `cli`, `storage`, `collector`, `factor`, `order`, and `account`

#### Scenario: Source repository mapping
- **WHEN** existing repositories are migrated
- **THEN** `moox/server` SHALL map to `modules/control`, `moox/cli` SHALL map to `modules/cli`, `xData-mini/storage` SHALL map to `modules/storage`, `data-collector` SHALL map to `modules/collector`

#### Scenario: Data miner capabilities are absorbed
- **WHEN** `data-miner` code is evaluated during migration
- **THEN** useful exchange client, symbol discovery, scheduling, rate limiting, retry, and source-normalization capabilities SHALL be merged into `modules/collector/internal/source`, `modules/collector/internal/discovery`, or `modules/collector/internal/scheduler`
- **THEN** the repository SHALL NOT create `modules/miner` or a `moox-miner` binary in the first-stage module layout

### Requirement: Command entrypoint naming
Each Go module that produces a binary SHALL use a command directory named after the final binary.

#### Scenario: CLI command entrypoint
- **WHEN** the CLI module is migrated
- **THEN** its command entrypoint SHALL be `modules/cli/cmd/moox-cli/main.go`

#### Scenario: Service command entrypoints
- **WHEN** service modules are migrated
- **THEN** their command entrypoints SHALL use names such as `cmd/moox-server`, `cmd/moox-storage`, `cmd/moox-collector`, `cmd/moox-factor`, `cmd/moox-order`, and `cmd/moox-account`

### Requirement: Storage naming clarity
The repository SHALL use `storage` for code that implements storage abstractions and SHALL reserve `var` or `testdata` for actual data files.

#### Scenario: Runtime data directory
- **WHEN** a module needs local runtime data such as db files, indexes, cache, or temporary files
- **THEN** the data SHALL be placed under root `var/` or a module-specific ignored runtime directory, not under `internal/data`

#### Scenario: Storage abstraction directory
- **WHEN** code implements storage routing, storage engines, or storage interfaces
- **THEN** the code SHALL use `internal/storage` or a more specific storage package name
