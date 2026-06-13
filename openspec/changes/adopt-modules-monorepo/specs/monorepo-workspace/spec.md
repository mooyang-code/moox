## ADDED Requirements

### Requirement: Workspace monorepo root
The repository SHALL expose moox as a workspace monorepo root that contains shared documentation, OpenSpec changes, skills, build scripts, configuration, deployment assets, and business modules.

#### Scenario: Root directories exist
- **WHEN** the first-stage monorepo skeleton is created
- **THEN** the repository root SHALL contain `docs/`, `openspec/`, `skills/`, `build/`, `modules/`, `configs/`, `schema/`, `deployments/`, and `var/`

#### Scenario: Business code is not root-sprawled
- **WHEN** a Go business component is migrated into moox
- **THEN** its source code SHALL live under `modules/<module-name>/` instead of being added as a new root-level business directory

### Requirement: Go workspace mode
The repository SHALL use `go.work` during the first migration phase and SHALL keep migrated Go components as separate Go modules.

#### Scenario: Workspace includes migrated modules
- **WHEN** a module is migrated into `modules/`
- **THEN** root `go.work` SHALL include that module path

#### Scenario: Module independence is preserved
- **WHEN** a migrated module has its own `go.mod`
- **THEN** the module SHALL remain independently testable with its module-specific test command

### Requirement: Single module convergence is deferred
The repository SHALL NOT require a single root `go.mod` during the first migration phase.

#### Scenario: Storage module has CGO constraints
- **WHEN** `modules/storage` is migrated
- **THEN** its RocksDB and DuckDB CGO build constraints SHALL remain isolated to the storage module build and test commands
