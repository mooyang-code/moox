## ADDED Requirements

### Requirement: Root build scripts
The repository SHALL centralize cross-module build, test, release, and skill packaging scripts under root `build/`.

#### Scenario: Build directory is created
- **WHEN** first-stage build infrastructure is added
- **THEN** root `build/` SHALL contain scripts for build, test, release, and skill packaging

#### Scenario: Root Makefile delegates
- **WHEN** a user runs common root make targets
- **THEN** the root `Makefile` SHALL delegate to scripts under `build/` instead of embedding complex build logic directly

### Requirement: Module-specific build strategies
The build system SHALL allow each module to keep its required build strategy while exposing common root commands.

#### Scenario: CLI cross-platform build
- **WHEN** release packaging is requested
- **THEN** `moox-cli` SHALL be built for Linux amd64, Darwin amd64, Darwin arm64, and Windows amd64

#### Scenario: Storage CGO build
- **WHEN** storage tests or builds are invoked
- **THEN** the build system SHALL use the storage module's CGO-aware test and build command instead of assuming plain `go test ./...`

### Requirement: moox Agent skill package
The repository SHALL provide a moox-specific Agent skill under root `skills/moox`.

#### Scenario: Skill references exist
- **WHEN** `skills/moox` is created
- **THEN** it SHALL include `SKILL.md` and references for build, storage, protocol, and release operations

#### Scenario: Skill package includes CLI
- **WHEN** the skill package is built
- **THEN** the package SHALL include `skills/moox` content and all required `moox-cli` platform binaries

### Requirement: Remote deployment acceptance
The repository SHALL provide a repeatable deployment and acceptance flow for the first-stage monorepo.

#### Scenario: Deploy to acceptance host
- **WHEN** `REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' make deploy` is executed after release packaging
- **THEN** moox module binaries SHALL be published under `~/moox/bin`
- **THEN** module configuration SHALL be published under `~/moox/configs/<module>`
- **THEN** module runtime data SHALL be stored under `~/moox/var/<module>`
- **THEN** module logs SHALL be stored under `~/moox/var/log/<module>`
- **THEN** the deploy script SHALL resolve `~/moox` on the remote host rather than expanding it to the local user's home directory

#### Scenario: CSV acceptance writes market data
- **WHEN** `REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' APT_CSV=/Users/mooyang/Downloads/APT-USDT.csv AR_CSV=/Users/mooyang/Downloads/AR-USDT.csv make acceptance` is executed
- **THEN** the acceptance flow SHALL upload both CSV files to `~/moox/var/storage/acceptance`
- **THEN** the acceptance flow SHALL write APT-USDT and AR-USDT K-line rows into xData/storage
- **THEN** the acceptance flow SHALL query both instruments back and fail if either imported row count or queried row count is zero
