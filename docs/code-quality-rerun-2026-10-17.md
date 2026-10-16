# Fresh production review — 2026-10-17

## Scope and conclusion

The baseline was merged main `dbd2db892374176cedc306e655c9fd8264326854`.
Two independent Cier / GPT-6.1 Sol high reviews covered authentication and
storage/metadata, while the parent repeated full tests, race checks, mutation
proof, compiled-server HTTP proof, and dependency vulnerability analysis.
Only local temporary databases, files, and server processes were used.

The baseline passed its normal/race suites, all 24 existing mutation probes,
and all 19 existing HTTP proof outcomes. `govulncheck` reported no reachable
vulnerabilities. Nevertheless, six defects were reproduced. Those checks proved
their stated behaviors; they did not establish that the repository had no
security, correctness, or data-integrity defects.

## Confirmed defects and resulting behavior

| Priority | Defect reproduced on the baseline | Fix and regression evidence |
| --- | --- | --- |
| P1 | A valid admin SigV4 management request signed to create a member accepted a replacement body creating an admin. Possession of that valid signed request was required; the signing secret was not. | The authenticated payload hash travels with the principal. Bounded JSON/XML readers compare the full consumed body before mutation. Production-router tests check valid bodies, changed roles, missing bodies, unchanged user counts, and header/query signatures. |
| P1 | Request cancellation near SQLite COMMIT returned an error after the row persisted. PUT then removed the new backing file, leaving GET returning 500. The reviewer reproduced this five times and with the race detector. | Check cancellation before COMMIT, then complete COMMIT without request cancellation. Deterministic driver-boundary tests use real SQLite before/after its commit; real disk/PUT tests verify metadata and bytes remain consistent for creation and overwrite. Deferred constraint failures still roll back. |
| P2 | A presigned PUT authenticated a query payload digest but accepted changed bytes, because only a header digest reached streaming checksum validation. | Stream validation checks the authenticated digest as well as any supplied header. Object/part tests cover valid, changed, conflicting, and explicitly unsigned payloads, preserving previous bytes on rejection. |
| P2 | Management bucket deletion removed object files and then failed with a foreign-key error when a multipart upload remained. | One write transaction checks all multipart states and deletes object/bucket rows. Existing uploads cause HTTP 409 before any data changes; concurrent upload creation is serialized. Captured files are cleaned after commit, with cache invalidation. |
| P2 | V1 marker / V2 start-after inside a directory returned that directory's common prefix before the marker, incorrectly consuming the page limit. | Filter grouped virtual keys against the marker before LIMIT. Metadata and HTTP tests cover both API versions, zero/small limits, Unicode prefixes and multi-character delimiters. |
| P2 | Bucket creation, multi-delete, and multipart completion accepted a second XML root or trailing text and performed the first document's mutation. | Shared XML framing checks require one root and consume its tail to EOF. Six HTTP regressions verify bucket absence, preserved object bytes, and retained multipart uploads. Parser cases retain valid declarations, BOMs, comments, processing instructions, and namespaces. |

## Reproducible verification

Use the CI Go toolchain locally:

```bash
export GOTOOLCHAIN=go1.27.2
export GOEXPERIMENT=
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
python3 scripts/verify-test-regressions.py
python3 scripts/prove-production-audit.py
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

The regression script requires each baseline test to pass and each source
mutation to fail at the intended assertion. Compile errors do not count as
proof. The HTTP script runs a normal compiled server with temporary data and
prints no credentials; its SigV4 signer is independent of the Go implementation.
The updated proof passes 33 mutation probes and 42 compiled-server HTTP
outcomes. Full normal/race suites, vet, staticcheck in both build modes, normal
and test-endpoint builds, module hygiene, and deadcode pass locally. The fresh
dependency scan still reports zero reachable vulnerabilities (four findings are
in required modules outside imported packages). CI publishes both scripts'
output in the existing regression-proof artifact.

## Boundaries

`UNSIGNED-PAYLOAD` deliberately leaves the body unbound and remains supported.
Control body limits and streaming object/part uploads remain in effect.
After a COMMIT starts, filesystem stalls are not cut short using the request
context: that would reintroduce an uncertain commit result. SQLite's busy timeout
continues to bound database lock waits. Power loss, device/filesystem failures,
and ambiguous non-cancellation commit failures require separate fault injection.

Management deletion returns 409 for uploads in every state. A five-second,
independent file-cleanup phase follows successful metadata deletion. Disk errors
can still return 500 and leave orphan files for startup reconciliation; there
is no distributed transaction spanning SQLite and the filesystem.

No production deployment, external customer endpoint, or destructive external
test was part of this review. Passing this round narrows known risks and adds
regressions; it is not a blanket assurance of repository quality.

## Primary references

- [Go context.WithoutCancel](https://pkg.go.dev/context#WithoutCancel) documents cancellation detachment.
- [Go XML decoder](https://pkg.go.dev/encoding/xml#Decoder.Decode) documents decoding an element; complete request framing needs an EOF check.
- [AWS ListObjectsV2](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html) specifies filtering common prefixes against StartAfter.
