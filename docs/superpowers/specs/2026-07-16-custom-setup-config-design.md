# Custom Setup Configuration Design

## Context

A fresh MooX installation needs four classes of user-supplied data before the
rest of the system can be configured:

- the first Admin login name and password;
- Tencent Cloud API `SecretId` and `SecretKey`;
- one control host used to deploy Admin, Gateway, and Web;
- zero or more additional SSH hosts that later deployment steps may use.

Today these values are collected through separate commands and deployment
flags. That exposes credentials to whichever operator or Agent assembles the
commands. The new flow makes the repository-root `moox.toml` the only
user-authored setup input. High-level CLI commands consume the file without
printing its sensitive fields.

`moox.toml` is not an application runtime configuration file. It is a
persistent, user-owned setup manifest. MooX never modifies, renames, or
deletes it.

## Goals

- Let the user write every initial credential directly into `moox.toml`.
- Let an Agent run validation, control-plane deployment, and initialization
  without reading or reproducing those credentials.
- Deploy only Admin, Gateway, and Web during the first deployment stage.
- Store the first super administrator, Tencent Cloud credential, control host,
  and other hosts atomically in Admin.
- Make initialization safe to retry with the same manifest and reject changed
  manifests after completion.
- Keep setup write access off the public Admin and Gateway routes.
- Update the MooX Skill so it leads the user through this exact sequence before
  asking where to deploy the remaining services.

## Non-Goals

- Defining the placement of Storage, CloudNode, Collector, Factor, Monitor,
  Trade, or other data-plane services.
- Supporting SSH certificate authentication in `moox.toml`.
- Supporting multiple setup file formats or format versions.
- Rotating credentials through `setup apply`.
- Preserving compatibility with existing deployment flags that create the
  first Admin user.
- Copying `moox.toml` to a deployment host.

## Configuration Contract

The repository-root template is:

```toml
[admin]
username = "admin"
password = ""

[tencent_cloud]
secret_id = ""
secret_key = ""

[control_host]
name = "control"
address = ""
port = 22
username = "ubuntu"
password = ""

[[other_hosts]]
name = "compute-1"
address = ""
port = 22
username = "ubuntu"
password = ""
```

The parser accepts exactly these tables and fields. Unknown or duplicated
fields fail validation. `other_hosts` may be absent or empty. All other fields
are required; `port` defaults to `22` when omitted.

Host names and addresses must be unique across `control_host` and
`other_hosts`. The first release supports password authentication only. The
Admin password follows the bcrypt 72-byte limit already enforced by
`moox-admin-cli`.

`moox.toml` must:

- resolve to `<repository-root>/moox.toml`;
- be a regular file rather than a symbolic link;
- be owned by the current user;
- have mode `0600`;
- remain unchanged for the duration of each command.

MooX adds `/moox.toml` to `.gitignore`. Commands open the file read-only and
compare its identity, size, modification time, and SHA-256 digest before and
after the operation. They never repair its permissions or rewrite its content.

## Command Surface

The CLI adds one top-level command group:

```text
moox-cli setup validate --file ./moox.toml
moox-cli setup trust-host --file ./moox.toml --host control --fingerprint <sha256>
moox-cli setup deploy-control --file ./moox.toml
moox-cli setup apply --file ./moox.toml
moox-cli setup status --file ./moox.toml
moox-cli setup hosts --file ./moox.toml
moox-cli setup deploy-storage --file ./moox.toml --host <host-name>
moox-cli metadata spaces --file ./examples/metadata-quant-initial.seed.yaml
moox-cli setup metadata-import --file ./moox.toml --storage-host <host-name> --spaces <space-id,...>
```

`--file` defaults to `./moox.toml`, but the resolved path must still be the
repository-root file. This prevents an Agent from silently substituting a
different manifest.

### `setup validate`

Validation performs four ordered phases:

1. Validate file location, ownership, mode, stable snapshot, TOML structure,
   field lengths, and uniqueness.
2. Call a read-only Tencent Cloud identity API with the configured
   `SecretId`/`SecretKey`.
3. authenticate to `control_host` with SSH password authentication.
4. authenticate to each `other_hosts` entry.

The command prints one sanitized result per phase. Host results contain only
the configured host name and a stable status or error code. Tencent results may
contain the account UIN returned by the identity endpoint, but no request
signature or credential fragment.

### `setup deploy-control`

This command runs deployment validation (the immutable manifest and SSH host
checks, without Tencent Cloud STS), builds the release, and deploys only:

- `moox-admin` and `moox-admin-cli`;
- `moox-gateway` and `moox-gateway-cli`;
- `moox-web-host` and managed Caddy assets;
- the setup loopback listener and generated server-side encryption keys.

The command uses `control_host` for target address, port, username, and
password. It does not pass the password through argv. The deployment transport
is owned by Go code and uses SSH/SFTP directly; the existing deployment script
is split so packaging remains reusable while remote transport no longer
depends on shell `ssh`/`scp` password handling.

The control deployment does not create the first Admin user. It waits for
Admin readiness, setup listener readiness, Gateway readiness, Web
readiness, and the browser HTTPS endpoint before returning success.
Tencent Cloud identity validation is intentionally not repeated here. The
read-only STS check belongs to `setup validate` and cloud-resource commands;
deploying control-plane binaries itself only needs the configured SSH access.
The remote deployment directory is fixed to `~/moox/prod` for this first-stage
workflow. Later deployment commands may expose explicit placement options, but
the setup manifest does not grow deployment-tuning fields.

### `setup apply`

The CLI validates the manifest again, establishes an SSH connection to the
control host, and opens a local port forward to the Admin setup listener on
`127.0.0.1`. The manifest is serialized directly from memory into the forwarded
request. It is never uploaded as a file.

Admin executes one database transaction that:

1. creates the first active super administrator with a bcrypt password hash;
2. creates the fixed `tencent-default` credential in Admin Secret Management;
3. creates the control host in SSH Host Management;
4. creates every additional host.

The Tencent credential uses:

```text
secret_id:    tencent-default
name:         Tencent Cloud Default
category:     cloud
provider:     tencent
secret_type:  api_key
key_id:       configured SecretId
secret_value: configured SecretKey
status:       active
```

Admin encrypts `secret_value` and every SSH password with the existing Admin
encryption key. The setup service must use transaction-scoped repositories
so all initial records commit or roll back together.

### `setup status`

Status loads the same immutable `moox.toml` snapshot and sends it through the
SSH tunnel for a read-only comparison against the actual Admin records. It
returns only `completed`, `incomplete`, or `conflict` plus sanitized counts.
Credentials are used only for bcrypt or decrypted-value comparison and never
appear in the response or logs.

### `setup hosts` And `setup deploy-storage`

Storage placement begins only after `setup status` reports `completed`.
`setup hosts` returns the manifest host name, address, port, username, and role;
it never returns an SSH password. The Skill asks the user to choose one of these
hosts. The CLI does not infer a default, so `setup deploy-storage --host` is
required even when the user chooses `control_host`.

The Storage deployment is one indivisible initial unit containing
`storage-access` and the unified `storage-view` service. Both processes run on
the selected host. `storage-view` owns indexing, materialization, and query
behind one `trpc_go.yaml`. The unit is
installed under `~/moox/storage`, separate from the control deployment at
`~/moox/prod`, so choosing the control host cannot replace Admin, Gateway, or
Web. After readiness succeeds, the CLI updates the Storage service placement
through the private SysDeploy endpoint on the control host.

`setup deploy-storage` uses the same deployment validation as
`deploy-control`; it does not require a Tencent Cloud STS call. Tencent
credentials remain required for the full `setup validate` command and for
cloud-resource operations, but are not a prerequisite for copying and
starting Storage binaries over SSH.

### Metadata Space Selection And Import

Business metadata initialization is a separate, optional step. It does not add
fields to or modify `moox.toml`. `metadata spaces` lists the stable Space IDs,
names, and descriptions in the default seed without connecting to a server.
The Skill presents that list in natural language and lets the user select all,
some, or none.

For a non-empty selection, `setup metadata-import` first verifies that control
setup is complete, then connects to the explicitly named Storage host and
forwards its loopback Metadata API through SSH. Filtering retains the complete
dependency closure for each selected Space, including its data sources,
datasets, views, fields, columns, subjects, and shared global storage
resources. Unknown or duplicate Space selections fail before any import call.

### Storage Schema v5 Reset And Verification

Storage deployment is a clean-break Schema v5 replacement for this pre-release
project. `deploy-storage` keeps `--reset-storage-data` false by default. The
operator must explicitly confirm a pre-production reset before using it; the
option is not a production migration mechanism or a normal redeploy shortcut.
When enabled, the remote installer removes only the Storage data directory and
recreates it from the current package. The remote Storage `secrets/` directory
is preserved, and the CLI never writes or deletes `moox.toml`.

After deployment, the setup CLI provides three explicit verification boundaries:

```text
setup verify-storage --file ./moox.toml --host <storage-host>
setup e2e-storage --file ./moox.toml --host <storage-host> --namespace <short-id>
setup browser-e2e-storage --file ./moox.toml --host <storage-host> --repo-root <repo>
```

`verify-storage` is read-only. It uses the setup SSH tunnel and signed service
requests to report only component readiness, exact commit, binary hashes,
Schema version, DataNode identity/status, counts, and the absence of route RPCs.
`e2e-storage` uses a caller-supplied namespace, creates a disabled temporary
Space/DataSource/Dataset, checks and activates it with revision CAS, verifies
the metadata lifecycle through supported APIs, and cleans up the temporary
resources even after an assertion failure. Its JSON contains only sanitized
IDs, check IDs, revisions, statuses, and cleanup state.

`browser-e2e-storage` runs the remote Playwright spec against the named Admin
UI and covers DataNode and Dataset workflows at desktop and 390px mobile
viewports. The setup process sends `base_url`, username, and password through
the child process stdin to `remote-auth-global-setup.ts`; credentials remain
in memory for the run and are never placed in argv, logs, artifacts, or
browser storage. Remote mode registers only the remote project, disables
trace/video, leaves `storageState` unset, and does not start a local web server.
The browser spec uses the deployed API rather than synthetic fixtures and does
not perform destructive Dataset or DataNode mutations.

## Admin Setup Service

Admin gains a dedicated `trpc.moox.admin.Setup` service on a fixed
loopback-only HTTP listener. Managed Caddy, the browser Admin gateway, and the
node Gateway must not route to this service.

The service exposes two methods:

```text
ApplySetup(SetupManifest) -> SetupResult
GetSetupStatus(SetupManifest) -> SetupStatus
```

The listener validates that the accepted connection is local. Deployment
configuration binds it to `127.0.0.1`; startup fails if configuration attempts
to bind it to a non-loopback address.

`ApplySetup` derives state from the actual user, secret, and host rows; it does
not maintain a separate setup-state table:

- every expected row is absent: create all rows and return `created`;
- existing rows match and some expected rows are absent: create only the
  missing rows in the same transaction and return `created`;
- every expected row exists and matches: return `unchanged`;
- any existing expected row conflicts: return `setup_conflict` and change
  nothing.

The configured Admin account must be the first active super administrator. If
the configured row is absent while another active super administrator exists,
setup reports a conflict instead of creating a second initial Admin. Once the
configured Admin exists, unrelated administrators created later are ignored.
Admin passwords are compared with bcrypt; Tencent SecretKey and SSH passwords
are decrypted only in memory for constant-time comparison. Existing unique
constraints protect usernames, host addresses, and the fixed Tencent credential.
Concurrent apply calls re-read after uniqueness conflicts so they resolve to
`unchanged` or `setup_conflict`, never duplicate rows.

`GetSetupStatus` runs the same comparisons without writes. Missing rows produce
`incomplete`; mismatched rows produce `conflict`; only a complete exact match
produces `completed`. Unrelated records added later through Admin do not affect
the result.

## Deployment Boundary

The current `scripts/deploy/deploy-moox.sh` creates the first Admin user before service
startup. That behavior moves out of deployment. The deprecated
`--admin-username` and `--admin-password-file` flags are removed rather than
retained as a second setup path.

Control-plane packaging becomes an explicit profile instead of a long list of
negative flags:

```text
scripts/deploy/deploy-moox.sh --profile control ...
```

The `control` profile includes Admin, Gateway, Web, and managed edge assets. It
excludes Storage, Archive, EventBus, CloudNode, Collector, Factor, Monitor,
Strategy, Trade, and HostAgent. Later Skill steps deploy those services after
the user chooses hosts from Admin SSH Host Management.

The high-level CLI invokes the packaging profile and owns credential-bearing
remote transport. The shell script never parses TOML and never receives a host
or Admin password.

## Security Rules

- Never print, log, marshal into an error, or include in tracing the Admin
  password, SSH passwords, Tencent `SecretId`, or Tencent `SecretKey`.
- Do not print masked credential fragments; even partial values are needless.
- Do not put sensitive values in argv, environment variables, release archives,
  stage directories, process titles, shell history, or temporary files.
- Keep `moox.toml` open only while taking an immutable in-memory snapshot.
- Zero mutable secret byte slices when a command finishes where practical.
- Wrap Tencent and SSH errors in stable, sanitized error codes.
- Reject symlinks and permission changes observed during execution.
- Keep the setup API outside all public route tables.
- Require host key verification. On first use, show the control host SHA-256
  fingerprint and require the user to add it to the local MooX known-hosts file;
  non-interactive Agent execution must never auto-accept an unknown host key.
- Store MooX setup known hosts under `~/.config/moox/known_hosts` with mode
  `0600`; do not modify the user's global OpenSSH file.

## Failure Handling

Validation failures stop before build or deployment. A failed control-plane
deployment does not call `setup apply`. A failed apply transaction changes no
Admin data. A lost response after commit is resolved by rerunning the same
command; comparison with the stored records returns `unchanged`.

Stable error classes are:

```text
config_invalid
config_insecure
config_changed
tencent_auth_failed
host_key_unknown
ssh_auth_failed
ssh_unreachable
control_deploy_failed
control_not_ready
setup_not_reachable
setup_conflict
setup_storage_failed
verification_failed
```

Errors may include a host name or configuration field path. They must not
include serialized requests, credentials, authorization headers, remote shell
commands containing secrets, or decrypted database values.

## MooX Skill Flow

The Skill must:

1. If `moox.toml` is absent, show the exact template and stop so the user can
   fill it and set mode `0600`.
2. Never read the file with `cat`, `sed`, `rg`, a language parser, or an Agent
   tool. Only the high-level CLI may open it.
3. Run `moox-cli setup validate` and report sanitized results.
4. Stop for an unknown SSH host key and ask the user to verify and trust the
   displayed fingerprint through the dedicated trust command.
5. Run `moox-cli setup deploy-control`.
6. Require signed readiness success for Admin, Gateway, Web, and the managed
   browser edge.
7. Run `moox-cli setup apply`, then `setup status`.
8. Let `setup apply` verify the public login API from the in-memory manifest;
   ask the user to confirm interactive browser login when required.
9. Tell the user that `moox.toml` remains unchanged and still contains
   plaintext credentials.
10. Run `setup hosts` and ask the user in natural language which listed host
    should run Storage. Do not silently default to the control host.
11. Run `setup deploy-storage --host <selected-name>` and require readiness for
    both Storage processes before continuing.
12. Run `metadata spaces` against the default seed, present the available
    business spaces, and let the user choose all, some, or no spaces.
13. If the selection is non-empty, translate names to stable Space IDs and run
    `setup metadata-import` with the selected Storage host and Space IDs.
14. Keep `moox.toml` initialization and metadata import as visibly separate
    steps. Never write deployment placement or metadata choices into the file.
15. Treat `--reset-storage-data` as an explicit pre-production-only action;
    never infer it from a Schema mismatch or retry it silently.
16. Run `verify-storage`, `e2e-storage`, and `browser-e2e-storage` only through
    the setup CLI. Keep their JSON sanitized and do not persist browser state.

## Tests And Acceptance

Automated tests must cover:

- strict TOML decoding, empty optional host list, unknown fields, duplicate
  names/addresses, bcrypt length, root-path enforcement, symlink rejection,
  ownership, `0600`, and file mutation detection;
- Tencent read-only validation and redaction of SDK errors;
- SSH password authentication, host key trust, per-host sanitized failures, and
  no credential bytes in captured stdout/stderr;
- control profile contents and proof that the deploy script no longer creates
  an Admin user;
- loopback-only setup binding and absence from Caddy/Admin/Gateway routes;
- one-transaction creation of the user, cloud secret, and all hosts without a
  separate setup-state table;
- rollback at every write boundary;
- exact retry, partial completion, adoption of matching existing records, and
  rejection of conflicting existing rows;
- encrypted database values and bcrypt password hashing;
- `moox.toml` byte-for-byte stability across every command;
- sanitized host listing and mandatory explicit Storage placement;
- one-unit Storage packaging, separate remote directory, readiness checks, and
  private SysDeploy placement updates;
- metadata Space catalog output, selection dependency closure, unknown and
  duplicate selection rejection, and SSH-tunneled import;
- Skill contract checks that forbid direct file-reading commands and enforce
  validate, control deploy, apply, status, Storage placement, and metadata
  selection/import ordering.

End-to-end acceptance uses disposable local SSH and Tencent validator fakes,
then a real remote control host:

1. `setup validate` reports every section valid without exposing a secret.
2. `deploy-control` publishes only Admin, Gateway, Web, and Caddy.
3. The setup listener is reachable through SSH forwarding and unreachable
   through public HTTPS routes.
4. `setup apply` returns `created`; a second run returns `unchanged`.
5. Admin contains one super administrator, one active Tencent credential, and
   all configured hosts with encrypted sensitive columns.
6. The public login API accepts the configured account, and the user can log in
   through the browser.
7. `moox.toml` has the same bytes, owner, and `0600` mode after the workflow.
8. The user can choose control or another listed host; both Storage processes
   become ready there without replacing the control deployment.
9. The metadata catalog is shown only after Storage readiness, and importing a
   subset creates resources only for the selected Spaces and their complete
   dependencies.
10. Choosing no metadata Spaces performs no import and leaves `moox.toml`
    unchanged.
