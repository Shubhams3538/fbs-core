# Production correctness and test quality audit — 2026-10-16

The small cleanup in #45 did not establish that the repository was production-ready.
A broader review reproduced failures in transaction cleanup, cache publication,
request parsing, conditional writes, migration compatibility, checksums, and
Unicode listings. The fixes are split into reviewable layers:

1. [#46 — request validation and authentication](https://github.com/i-got-this-faa/fbs-core/pull/46), based on #45.
2. [#47 — storage and metadata correctness](https://github.com/i-got-this-faa/fbs-core/pull/47), based on #46.
3. The test/refactor layer containing this report, based on #47.

Merge the stack from the bottom, starting with #45. These findings describe the
reviewed code at `4c30f0d`; they are not a claim that every remaining defect has
been found. Severity below is an engineering prioritization, not a CVSS rating.

## Verified production defects

| Priority | Reproduction / impact | Result after fix | Proof |
| --- | --- | --- | --- |
| P1 | Canceled multipart transactions returned an open transaction to the pool. A later object write returned success, then disappeared when another operation rolled that transaction back. | Independent bounded rollback; uncertain or failed-cleanup connections discarded. All manual BEGIN paths share the helper. | `transaction_test.go`: cancellation boundaries, acknowledged writes, failed-cleanup disposal. |
| P1 | A delayed cache miss published an old object after an overwrite had committed and removed its backing file. Later reads used that missing file indefinitely. | Mutations evict; misses publish only against an unchanged revision, otherwise re-read. Multipart completion invalidates instead of publishing a possibly superseded row. | `cache_consistency_test.go`: barriers around overwrite/delete/empty/bucket deletion/external commit, actual files; owner-transfer race. |
| P1 | Unbounded control bodies could consume memory. JSON decoders accepted an initial document followed by garbage or another value; multi-delete could act on a truncated prefix. | JSON 1 MiB, bucket XML 4 KiB, multipart-complete/delete XML 2 MiB; JSON requires one complete value. Both known-length and chunked overflow rejected. | HTTP/setup/management/S3 body tests; invalid requests preserve rows, bytes, and multipart retry. |
| P1 | PUT and CopyObject ignored destination conditions and overwrote existing bytes. Checking only before file writing would still race. | Destination check and upsert share one write transaction; cleanup uses the actual replaced path. | PUT/copy status-and-byte assertions; 12 concurrent create-only writes produce one winner. |
| P2 | Bearer rejected the field count of a SigV4 header, masking inactive-user and repository errors. | Other schemes are skipped before Bearer token validation. | Real signed SigV4 chain tests preserve inactive/invalid/internal failures. |
| P2 | Migration 11 added a nullable multipart checksum algorithm that its scanner read into a string. Legacy uploads could not be listed, completed, aborted, or cleaned. | NULL is read as no selected algorithm. Works for already-upgraded rows too. | Real version-10-shaped upgrade; GetByID, ListByBucket, ListStale and cleanup. |
| P2 | CRC64-NVME used an unreflected polynomial with Go's reflected implementation. Correct AWS checksums were rejected and the wrong checksum accepted. | Uses reflected polynomial `0x9a6c9329ac4bc9b5`. | Three AWS known-answer vectors; real object/part writes, persistence and rejection cases. |
| P2 | Go byte lengths were used as SQLite character offsets, hiding Unicode-prefixed objects. Multi-character delimiters were truncated. | Character counts and complete delimiter lengths are used consistently. | Accented/CJK/emoji prefixes, ASCII/emoji/multi-character delimiters, grouping, cursor and limit tests. |
| P2 | A 1 KiB cache retained 128 KiB of user metadata while counting only a few hundred bytes. | Entry weights include metadata keys/values and checksums with map overhead. | Oversized metadata/checksum entries are evicted. Accounting remains approximate, not an allocator/RSS limit. |
| P2 | Cache copies shared their mutable user-metadata map with callers. | Maps are copied on insertion/publication and return. | Mutating inputs and returned metadata cannot change a later cached result. |

AWS checksum expectations come from [AWS's own implementation and vectors](https://github.com/awslabs/aws-checksums/blob/main/tests/crc64_test.c),
not from recomputing expected values with the production function.

## Test debt and structure addressed

- Compiled-server tests inherited arbitrary `FBS_*` settings. Startup reconciliation
  could delete files in the inherited data directory. The launcher now strips
  inherited settings, accepts explicit overrides, and isolates the working directory.
  A test-owned sentinel and database prove both remain unchanged.
- Process output capture is synchronized, readiness uses HTTP plus exit detection,
  and a single goroutine owns Wait. Cleanup always terminates and waits for children.
- The old deactivation test hit an admin-only route that denied an active member
  too. It now proves Bearer and SigV4 access before deactivation, stored inactive
  state, and rejection afterward.
- Bucket cleanup tests now write real backing files and assert their removal,
  separately from metadata deletion and bucket retention/removal.
- Shutdown tests now hold a real request in flight, proving drain and the configured
  shutdown deadline. A replacement of Shutdown with Close is caught.
- Setup status and bootstrap cover IPv4, IPv6, mapped addresses, malformed peers,
  and spoofed forwarded headers. Configuration tests assert actual environment/flag
  values and validation boundaries.
- Management handlers are split into keys, buckets, listing, public URLs, activity,
  and request-field parsing. Existing routes and response policies remain covered.

## Reproducible validation

Use Go 1.27.2 (matching go.mod/CI). In an environment with another toolchain or
GOEXPERIMENT override, prefix commands with `GOTOOLCHAIN=go1.27.2 GOEXPERIMENT=`.

```sh
go mod tidy -diff
go mod verify
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/fbs-audit ./cmd/server
go build -tags testendpoints -o /tmp/fbs-audit-tagged ./cmd/server
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 -tags testendpoints ./...
go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./...
python3 scripts/verify-test-regressions.py
python3 scripts/prove-production-audit.py
```

All checks above passed locally; deadcode printed nothing. The mutation script
rejects **22 regressions using temporary Go overlays** and checks the intended
assertion, rather than treating a compile failure as success. The isolated normal
compiled-server proof passes **19 HTTP checks**, including exact object bytes,
conditional failures, a published checksum, Unicode listing, chunked limits,
and active/inactive authentication. It emits no credentials and removes its
own temporary data. The `regression-proof` CI job runs both scripts and saves
their output as workflow artifacts. These scripts do not replace the normal and race suites.

`govulncheck` reported zero reachable vulnerabilities and zero in imported packages;
it also reported four in required modules that this program does not appear to
call. This is the scan result at audit time, not a future vulnerability guarantee.

## Limits and remaining work

The storage and test-debt findings were independently reproduced by Cier
GPT-6.1 Sol high subagents. Their implementation tasks hit that account's usage
limit after partial test/refactor edits; the parent completed the remaining work.
The separate authorization review was rejected by the provider's content filter,
so that area was reviewed by the parent. There was no completed independent
post-fix review; do not count CodeRabbit's quota-skipped response as one.

- No power-loss tests, filesystem failure injection, full Ceph compatibility run,
  production deployment, or performance/load benchmark was performed.
- SQLite still uses synchronous(NORMAL). A successful normal test does not prove
  durability across power loss.
- Cache revisions are process-local. External database writers are not observed;
  unrelated concurrent mutations can cause in-flight misses to retry. Entry
  weights estimate retained payload size and overhead, not total process memory.
- A reverse proxy connecting over loopback can make remote setup/dev requests look
  local. Keep setup inaccessible through the proxy and dev mode off outside local
  development; a stronger bootstrap trust model remains a separate change.
- Quotas/capacity controls, active-content isolation, claimed-upload crash recovery,
  and concurrent delete/write orphan-file handling remain broader design work.
  This audit does not declare those solved.

## 🌐 Community Wisdom: [SQLite WAL Does Not Mean Unlimited Writers](https://dev.to/chenyuan20509/sqlite-wal-does-not-mean-unlimited-writers-a-practical-python-playbook-2lkn)

> **Source**: [Chen Yuan](https://dev.to/chenyuan20509)
>
> **Tags**: `python`, `sqlite`, `database`, `backend`
>
> The author's practical guidance favors short write transactions and explicit
> rollback/connection ownership. It supports the cleanup approach here, but is
> not independent proof of this Go implementation. The post had no discussion
> comments when read; this is one relevant source, not a community consensus.
>
> 🔗 [Read Full Discussion](https://dev.to/chenyuan20509/sqlite-wal-does-not-mean-unlimited-writers-a-practical-python-playbook-2lkn)
