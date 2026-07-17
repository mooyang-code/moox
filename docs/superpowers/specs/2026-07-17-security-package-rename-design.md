# Security Package Rename Design

## Goal

Rename the shared Go module `packages/crypto` to `packages/security` so `crypto`
remains unambiguous in MooX business terminology: it denotes the cryptocurrency
market, while `security` denotes cryptographic and authentication primitives.

## Scope

This is a direct source-level rename with no compatibility layer:

- Directory: `packages/crypto` -> `packages/security`
- Module path: `github.com/mooyang-code/moox/packages/crypto` ->
  `github.com/mooyang-code/moox/packages/security`
- Go package declaration: `package crypto` -> `package security`
- Import alias: `mooxcrypto` -> `mooxsecurity`
- Workspace, direct and indirect module requirements, local replacements,
  package-boundary checks, and current architecture documentation must use the
  new path.

Historical implementation plans remain historical records and are not rewritten.
Static acceptance scans therefore exclude `docs/superpowers/plans/`.

## Package Boundary

The renamed package keeps its current responsibility and public API unchanged.
It continues to provide reusable security mechanics:

- AES-GCM encryption and decryption
- SHA-256 and HMAC-SHA256 helpers
- Cryptographically secure random values
- bcrypt password hashing and verification
- Generic HS256 JWT signing and parsing
- Secret masking

Business authorization, cryptocurrency-market concepts, configuration loading,
claim policy, and field-selection policy remain outside this package. The rename
must not change ciphertext formats, password hashes, token semantics, or runtime
behavior.

## Migration

Move the module atomically and update all consumers in the same change. Run
`go mod tidy` in every affected workspace module so both direct and transitive
references converge on `packages/security`. Do not leave a forwarding module,
type alias, wrapper package, or `replace` entry for the old path.

Use the natural package identifier `security` inside the module. Consumers that
need an explicit repository-qualified alias use `mooxsecurity`; this avoids both
the standard-library `crypto` namespace and the MooX cryptocurrency-market term.

## Documentation

Update the active architecture module map and current package documentation to
describe `packages/security` as the shared security-primitives module. Examples
and current operational documentation must not tell users to import the old path.

## Verification

Acceptance requires:

1. `go.work` contains `./packages/security` and not `./packages/crypto`.
2. No production Go source, current documentation, `go.mod`, or script references
   the old module path or `mooxcrypto` alias.
3. The renamed package tests pass and its public behavior remains unchanged.
4. All direct consumers and transitive workspace modules compile and test.
5. `make verify` passes.
6. The worktree is clean after commit and push, and `HEAD` equals `origin/main`.
