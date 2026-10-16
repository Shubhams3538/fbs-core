# Development

## Repository Layout

```text
cmd/server/              entrypoint, dependency wiring, routing, graceful shutdown, process tests
internal/auth/           authentication (Bearer, SigV4, dev mode) and request principals
internal/authz/          authorization evaluator (admin, bucket owner, resource grants)
internal/config/         config loading and validation
internal/http/           router and HTTP middleware
internal/iam/            shared Role and Action types (imports no internal package)
internal/management/     admin JSON API
internal/metadata/       SQLite repositories and cache wrappers
internal/objectops/      object operations shared by the S3 and management APIs
internal/publicread/     signed public read URL support
internal/responses/      shared JSON response helpers
internal/s3/             S3-compatible API handlers
internal/s3compat/       S3 region constant shared by setup, management, and S3
internal/setup/          first-start bootstrap API
internal/storage/        local disk storage engine
migrations/              SQLite migrations
docs/                    completed project documentation
compat/s3-tests/         optional ceph/s3-tests runner (not vendored)
```

## Tests

The repository targets Go 1.27. Docker builds and the Nix development shell
use the same minor version. Confirm the active toolchain before running local
checks:

```bash
go version
```

Run all tests:

```bash
go test ./...
```

CI also runs these lint checks. Run them before you push:

```bash
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 -tags testendpoints ./...
go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./...   # must print nothing
```

### Maintaining Tests

Metadata repository tests use the package-local `openTestDB` helper, which calls
the production `metadata.Open` with an isolated database in `t.TempDir()`. This
runs real migrations and enables the production SQLite pragmas and foreign keys.
Seed required users and buckets through repository helpers; do not copy table DDL
into repository tests. Migration-specific upgrade fixtures belong in `migrations`.

Authentication response tests pass errors through the real authenticator chain
and middleware to the production responders in `cmd/server` and
`internal/management`. Keep error propagation, status, challenge headers, response
formats, and private-error redaction assertions there. Router tests cover routing, CORS,
recovery, and preservation of the streaming interfaces.

Use small, package-local fixture helpers to hide irrelevant setup. Keep the input
that defines each scenario visible, especially malformed SigV4 requests. Combining
tests into a table must preserve distinct boundaries: an ordinary TTL limit and
integer overflow, for example, still need separate rows. Prefer checking behavior
over comparing defaults to the same constants used to initialize them.

Run the focused regression-sensitivity proof with Go and Python 3:

```bash
python3 scripts/verify-test-regressions.py
```

The script first checks that each focused test passes, then uses temporary Go
overlays to reject 33 known regressions across authentication, request framing,
transaction cleanup, cache publication and memory accounting, legacy multipart
upgrades, Unicode listings, published checksum vectors, conditional writes,
deactivation, backing-file deletion, process isolation, setup access, configuration,
and graceful shutdown.
It succeeds only when the intended test assertion catches each mutation, and it
leaves source files unchanged. The normal suite must also pass:

```bash
go test ./... -count=1
go test -race ./... -count=1
```

The process test launcher strips inherited `FBS_*` variables and gives each child
its own working directory. Supply configuration overrides explicitly; never run
startup reconciliation against a developer's inherited data directory.

For an isolated HTTP proof against the normal compiled server:

```bash
python3 scripts/prove-production-audit.py
# or: python3 scripts/prove-production-audit.py --binary /path/to/fbs
```

It checks 42 outcomes, including preserved bytes after rejected conditional
writes, the AWS CRC64-NVME vector, Unicode listings, bounded and correctly framed
control bodies, signed payload integrity, multipart-safe bucket deletion,
listing markers, and active/inactive member authentication. It prints no credentials, uses temporary
data, and shuts its child server down before returning. The CI `regression-proof`
job runs both scripts and uploads their output as `production-regression-proof`.

### External S3 compatibility suite (ceph/s3-tests)

Shared team harness under `compat/s3-tests/` (tracked in git). It clones
[ceph/s3-tests](https://github.com/ceph/s3-tests) into a local `.workdir/`
and runs it against a temporary fbs-core process (or an external endpoint).

```bash
./compat/s3-tests/run.sh --keep
# writes results/last-run.log + results/checklist.md + results/checklist.html

./compat/s3-tests/run.sh -- s3tests/functional/test_s3.py::test_bucket_list_empty
./compat/s3-tests/run.sh --full
```

Open the feature checklist after a run:

- `compat/s3-tests/results/checklist.md`
- `compat/s3-tests/results/checklist.html`

Default `--core` mode applies `compat/s3-tests/markers.core`. Why each
marker is dropped or kept is documented in
[`compat/s3-tests/markers.md`](../compat/s3-tests/markers.md) (mini-IAM grants
are not AWS IAM/policy/STS — those markers stay excluded). Product scope:
[`plan/s3-compatibility.md`](../plan/s3-compatibility.md).

This is discovery and regression against AWS-like expectations, not the CI
source of truth. Claimed fbs-core behavior is covered by `go test ./...`.
Full team instructions: [`compat/s3-tests/README.md`](../compat/s3-tests/README.md).

The test suite is package-focused and covers:

- Config parsing and validation.
- Router health, CORS, recovery, and auth behavior.
- Setup bootstrap behavior.
- Management endpoint contracts.
- Bearer, SigV4, dev-mode, principal, and middleware auth behavior.
- Authz evaluator (admin/owner/grants/prefix/list rules) and grant repository behavior.
- Metadata repositories, migrations, cache behavior, multipart state, users, buckets, objects, grants, and management queries.
- Storage writes, reads, deletes, path sanitization, and reconciliation.
- S3 bucket, object, multipart, checksum, grant-scoped access, and compatibility behavior.
- Public read signing.

## Adding S3 Operations and Actions

When implementing a new S3 data-plane operation:

1. Map it to an existing `iam.Action` in `internal/iam/iam.go`, or add a new action there and in `plan/access-control/access-control.md`.
2. If the action should be grantable, add it to `grantableActions` in the same file. Metadata validation and the Management API read that one list.
3. Call the shared authz evaluator from the handler with the correct action, bucket, object key, and list prefix.
4. Do not invent a private owner-only boolean path for normal bucket/object ops.

## Implementation Principles

The codebase keeps behavior separated by package:

- HTTP handlers translate protocol details to repository and storage calls; authorization decisions come from `internal/authz`.
- Metadata repositories own SQLite queries and transactional state changes.
- Storage owns filesystem paths, temp files, file assembly, and reconciliation.
- Auth owns credential parsing and principal creation.

For new behavior, search for existing helpers before adding new abstractions. Prefer extending a narrow repository or handler helper over adding broad cross-package utilities.

## Adding S3 Behavior

When adding S3 compatibility:

- Add dispatch rules in `internal/s3/dispatch.go` only when query routing changes.
- Keep S3 XML DTOs local to the handler file that owns the operation.
- Return S3-style XML errors through `WriteS3Error`.
- Add black-box HTTP tests for status codes, headers, XML bodies, and edge cases.
- Avoid coupling tests to private implementation details when protocol behavior is the important contract.

## Adding Management Behavior

When adding Management API endpoints:

- Register routes in `internal/management/routes.go`.
- Use JSON response DTOs from `internal/management/dto.go`.
- Use `writeError` for consistent error envelopes.
- Keep responses `no-store` unless there is a deliberate reason to cache.
- Add endpoint tests in `internal/management`.

## Adding Metadata

When schema changes are needed:

1. Add a migration in `migrations/migration.go`.
2. Keep migrations idempotent where existing deployments may have partial historical schema.
3. Add repository methods in `internal/metadata`.
4. Test migration behavior and repository behavior.

SQLite remains the source of truth. Do not make object existence depend only on files on disk.

## Adding Storage Behavior

Storage paths must always resolve under the configured data directory. Use existing path validation helpers and keep object backing files independent from user-provided keys unless there is a clear reason to change the consistency model.

Writes should preserve the current commit order:

1. Write and sync bytes to disk.
2. Rename into place.
3. Commit metadata.
4. Clean old files after successful metadata commit.
