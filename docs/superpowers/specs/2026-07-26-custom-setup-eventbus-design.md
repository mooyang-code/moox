# Custom Setup EventBus Configuration Design

## Context

`custom.toml` is the user-owned input for a fresh MooX installation. The
current manifest contains the initial Admin account, Tencent Cloud credential,
and SSH hosts, but it does not describe the EventBus address that Collector
SCF must reach.

Today an operator must set `MOOX_EVENTBUS_ENABLE_TLS` and
`MOOX_EVENTBUS_PUBLIC_IP` outside the setup workflow. This creates two setup
inputs and makes `setup deploy-control` produce a loopback EventBus URL unless
the operator remembers the extra environment variables.

The manifest should contain only public deployment facts. MooX must continue
to generate the private CA, server certificate, role tokens, and
`cloudnode-worker.yaml`.

## Goals

- Make the EventBus public address part of the immutable setup manifest.
- Let `setup validate` reject an address that cannot support Collector SCF.
- Let `setup deploy-control` configure the broker, service directory, TLS
  certificate, and exported role files from the same manifest snapshot.
- Keep EventBus tokens and private key material out of `custom.toml`.
- Preserve the rule that setup commands read `custom.toml` without modifying
  or copying it.

## Non-Goals

- Letting users choose EventBus usernames or tokens.
- Storing a CA certificate or private key in `custom.toml`.
- Supporting plaintext public EventBus access.
- Rotating an existing EventBus address without the existing destructive reset
  flow.
- Copying `custom.toml` into a release archive or deployment host.

## Approaches Considered

### Explicit EventBus table

Add an `[eventbus]` table with the public address, port, and TLS decision. This
keeps the public endpoint explicit and supports control hosts behind NAT or
with a separate public DNS name.

This is the selected approach.

### Derive the address from `control_host`

This removes one field, but assumes the SSH address is also reachable from
Tencent SCF. That fails for private SSH addresses, bastion hosts, and split
DNS.

### Store the complete worker credential

The user could provide the username, token, and CA path. This duplicates
SecretMgr, permits mismatched broker and client credentials, and exposes
generated secrets to the setup manifest. MooX will not support this approach.

## Configuration Contract

The manifest adds this table:

```toml
[eventbus]
public_address = "106.53.107.122"
port = 4222
tls_enabled = true
```

`public_address` accepts an IPv4 address or DNS hostname without a scheme,
path, whitespace, or port. The first version rejects IPv6 literals because the
deployment script does not yet render bracketed IPv6 URLs.

`port` defaults to `4222` when omitted and must be between `1` and `65535`.

`tls_enabled` must be `true`. A public plaintext EventBus would expose
CloudNode job traffic and credentials, while Collector SCF requires the
private CA path.

The strict TOML decoder continues to reject unknown fields. Error messages
name only the invalid field and never include manifest values.

`custom.toml.example` contains the same table with an empty public address,
port `4222`, and TLS enabled.

## Deployment Flow

`setup deploy-control` passes the validated EventBus values through typed
deployment options:

```text
custom.toml
  -> setup config Snapshot
  -> deploy.Options
  -> CommandPackager environment
  -> deploy-moox.sh
```

The packager sets these environment variables only for the child deployment
command:

```text
MOOX_EVENTBUS_ENABLE_TLS=1
MOOX_EVENTBUS_PUBLIC_IP=<eventbus.public_address>
MOOX_EVENTBUS_PORT=<eventbus.port>
```

The deployment script:

1. renders `tls://<public_address>:<port>`;
2. binds the broker to `0.0.0.0:<port>`;
3. writes the same URL to `eventbus.extra_config.nats_url`;
4. generates a certificate for the URL host;
5. generates role tokens in SecretMgr;
6. exports `cloudnode-worker.yaml` with that URL and a relative `ca.pem`.

The script passes `MOOX_EVENTBUS_PORT` through remote deployment just like the
existing TLS and public-address variables. It does not source or transfer
`custom.toml`.

## Security Boundary

The setup manifest stores no EventBus credential. The following values remain
system-generated:

- the fixed `cloudnode-worker` username;
- the worker token;
- the private CA and server private key;
- every other EventBus role token.

The Collector publish command still reads the generated
`cloudnode-worker.yaml` on the deployment host and injects URL, username,
password, and CA PEM into SCF environment variables. The SCF zip remains
credential-free.

## Error Handling

`setup validate` fails before network or deployment work when:

- `[eventbus]` is absent;
- `public_address` is empty or contains a scheme, path, port, or whitespace;
- `port` is outside the valid TCP range;
- `tls_enabled` is false.

`setup deploy-control` keeps the existing fail-fast and rollback behavior.
Changing the EventBus address after credentials exist continues to fail with
the existing instruction to use `--reset-data`.

## Testing

- Config tests cover valid IPv4 and DNS addresses, port defaulting, and every
  invalid field class.
- Template contract tests keep `custom.toml.example` and setup documentation
  aligned.
- Deployment tests assert that typed EventBus values reach the packager.
- Script contract tests assert URL rendering, broker port rendering, and
  remote propagation.
- Existing EventBus credential tests continue to verify the generated
  `cloudnode-worker.yaml` and its bind-only ACL.
- `./scripts/test-go-workspace.sh`, `./scripts/test-deploy-moox-eventbus.sh`,
  and `make verify-pr` remain the final repository checks.
