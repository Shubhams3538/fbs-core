#!/usr/bin/env python3
"""Verify that focused tests reject known regressions without editing sources."""

import json
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def run_test(package, test, overlay=None):
    command = ["go", "test", package, "-run", test, "-count=1"]
    if overlay is not None:
        command.append("-overlay=" + str(overlay))
    return subprocess.run(
        command, cwd=ROOT, capture_output=True, text=True, timeout=120
    )


def verify_regression(name, path, before, after, package, test, failure):
    source_path = ROOT / path
    original = source_path.read_text()
    if before not in original:
        raise RuntimeError(f"{name}: mutation target changed; update this probe")

    baseline = run_test(package, test)
    if baseline.returncode != 0 or "[no tests to run]" in baseline.stdout:
        raise RuntimeError(f"{name}: baseline test failed or missing\n{baseline.stdout}{baseline.stderr}")

    with tempfile.TemporaryDirectory(prefix="fbs-regression-") as directory:
        mutated_path = Path(directory) / source_path.name
        mutated_path.write_text(original.replace(before, after))
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source_path): str(mutated_path)}}))
        result = run_test(package, test, overlay)

    output = result.stdout + result.stderr
    if result.returncode != 1 or "--- FAIL:" not in output or failure not in output:
        raise RuntimeError(f"{name}: expected assertion did not reject mutation\n{output}")
    if source_path.read_text() != original:
        raise RuntimeError(f"{name}: source changed during verification")
    print(f"PASS: {name}: baseline passes; mutation rejected ({failure})", flush=True)


def main():
    verify_regression(
        "signed control body integrity", "internal/http/request_body.go",
        'expected != "" && expected != "UNSIGNED-PAYLOAD"', 'false',
        "./cmd/server", "^TestSignedManagementBodyIntegrity$", "status = 201, want 400",
    )
    verify_regression(
        "authenticated payload digest propagation", "internal/auth/sigv4.go",
        "SignedPayloadHash: payloadHash,", 'SignedPayloadHash: "",',
        "./cmd/server", "^TestSignedManagementBodyIntegrity$", "status = 201, want 400",
    )
    verify_regression(
        "presigned upload digest validation", "internal/s3/checksum.go",
        "principal.SignedPayloadHash}", "principal.SignedPayloadHash[:0]}",
        "./cmd/server", "^TestSignedUploadBodyIntegrity$/^object$/^query_changed$", "status = 200, want 400",
    )
    verify_regression(
        "grouped listing marker before limit", "internal/metadata/objects.go",
        "WHERE g.virtual_key > ?", "WHERE (? = '' OR 1 = 1)",
        "./internal/metadata", "^TestDelimitedListingFiltersGroupedKeysBeforeLimit$", "want page and lookahead",
    )
    verify_regression(
        "pending multipart deletion conflict", "internal/metadata/bucket_cleanup.go",
        "if hasUploads {", "if false {",
        "./internal/management", "^TestManagementDeleteBucketRejectsMultipartWithoutDeletingData$", "want 409",
    )
    verify_regression(
        "COMMIT result survives request cancellation", "internal/metadata/transaction.go",
        'tx.ExecContext(context.WithoutCancel(ctx), "COMMIT")', 'tx.ExecContext(ctx, "COMMIT")',
        "./internal/metadata", "^TestImmediateTransactionCancellationAtCommitKeepsResultConsistent$",
        "commit misreported cancellation",
    )
    verify_regression(
        "cancellation before COMMIT rolls back", "internal/metadata/transaction.go",
        "if err := ctx.Err(); err != nil {\n\t\treturn err\n\t}", "// missing cancellation check",
        "./internal/metadata", "^TestImmediateTransactionCanceledBeforeCommitRollsBack$",
        "want context.Canceled",
    )
    verify_regression(
        "XML framing before mutation", "internal/s3/request_xml.go",
        "return finishXMLRequest(decoder)", "return nil", "./internal/s3",
        "^TestXMLRequestsRejectInvalidFramingBeforeMutation$", "malformed XML status=200 want 400",
    )
    verify_regression(
        "real migration schema",
        "migrations/migration.go",
        "CHECK (role IN ('admin', 'member'))",
        "CHECK (role IN ('admin'))",
        "./internal/metadata",
        "^TestUserGetByID$",
        "CHECK constraint failed",
    )
    verify_regression(
        "production authentication challenge",
        "cmd/server/router.go",
        '\t\tw.Header().Set("WWW-Authenticate", `Bearer realm="fbs"`)\n',
        "",
        "./cmd/server",
        "^TestAuthErrorResponses$/^debug_JSON$/^missing$",
        "WWW-Authenticate",
    )
    verify_regression(
        "middleware authentication error propagation",
        "internal/auth/middleware.go",
        "\t\t\t\tonError(w, r, err)\n",
        "\t\t\t\tonError(w, r, ErrMissingAuth)\n",
        "./cmd/server",
        "^TestAuthErrorResponses$/^debug_JSON$/^internal$",
        "status = 401, want 500",
    )
    verify_regression(
        "SigV4 chain error propagation",
        "internal/auth/sigv4.go",
        "return s.authenticateHeader(r)",
        "return Principal{}, ErrInvalidCredentials",
        "./internal/auth",
        "^TestChainAuthenticator_Errors$/^malformed_sigv4$",
        "want auth: malformed bearer token",
    )
    verify_regression(
        "private authentication error",
        "cmd/server/router.go",
        'map[string]string{"error": "auth failed"}',
        'map[string]string{"error": err.Error()}',
        "./cmd/server",
        "^TestAuthErrorResponses$/^debug_JSON$/^internal$",
        "response exposes the private authentication cause",
    )

    verify_regression(
        "canceled transaction cleanup", "internal/metadata/transaction.go",
        "if !tx.committed {", "if false {", "./internal/metadata",
        "^TestImmediateTransactionRollbackSurvivesCancellation$", "canceled transaction committed",
    )
    verify_regression(
        "cache publication ordering", "internal/metadata/cache.go",
        "c.revision++", "// missing publication invalidation", "./internal/metadata",
        "^TestCacheMissCannotResurrectCommittedObjectVersion$", "stale cached object",
    )
    verify_regression(
        "cache metadata budget", "internal/metadata/cache.go",
        "size += 64 + int64(len(key)+len(value))", "size += int64(len(key)) + 0*int64(len(value))",
        "./internal/metadata", "^TestCacheBudgetIncludesUserMetadataAndChecksums$",
        "metadata larger than whole budget retained",
    )
    verify_regression(
        "cache metadata copy isolation", "internal/metadata/cache.go",
        "copied.UserMetadata = maps.Clone(object.UserMetadata)",
        "if false { copied.UserMetadata = maps.Clone(object.UserMetadata) }",
        "./internal/metadata", "^TestCachedObjectMetadataIsAnIndependentCopy$",
        "cache aliases caller metadata",
    )
    verify_regression(
        "legacy multipart NULL upgrade", "internal/metadata/multipart.go",
        "&checksumAlgorithm, &metaStr)", "&u.ChecksumAlgorithm, &metaStr)",
        "./internal/metadata", "^TestLegacyMultipartUpgradeRemainsReadable$",
        "converting NULL to string is unsupported",
    )
    verify_regression(
        "Unicode prefix listing", "internal/metadata/objects.go",
        "prefixLen := utf8.RuneCountInString(prefix)",
        "prefixLen := len(prefix); _ = utf8.RuneCountInString(prefix)",
        "./internal/metadata", "^TestDelimitedListingUnicodePrefixesAndDelimiters$", "entries=",
    )
    verify_regression(
        "published CRC64-NVME answers", "internal/s3/checksum.go",
        "crc64.MakeTable(0x9a6c9329ac4bc9b5)", "crc64.MakeTable(0xad93d23594c93659)",
        "./internal/s3", "^TestCRC64NVMEKnownAnswers$", "CRC64-NVME =",
    )
    verify_regression(
        "atomic destination write conditions", "internal/metadata/object_replace.go",
        "checkWritePreconditions(existing, ifMatch, ifNoneMatch)",
        'checkWritePreconditions(existing, "", "")',
        "./internal/s3", "^TestObjectWritesEnforceDestinationPreconditions$", "status=200 want=412",
    )
    verify_regression(
        "member deactivation", "internal/management/keys.go",
        "user.IsActive = *req.isActive", "user.IsActive = true",
        "./internal/management", "^TestManagementPatchKeyDeactivatePreventsBearerAuthentication$",
        "stored member is still active",
    )
    verify_regression(
        "real bucket backing-file deletion", "internal/objectops/cleanup.go",
        "if disk != nil {", "if false && disk != nil {",
        "./internal/management", "^TestManagement(DeleteBucket|EmptyBucket)RemovesObjects", "backing file",
    )
    verify_regression(
        "setup status peer restriction", "internal/setup/handlers.go",
        "if !isLoopbackRequest(r) {", "if false && !isLoopbackRequest(r) {",
        "./internal/setup", "^TestSetupRoutesRequireLoopbackPeer$", "want 403",
    )
    verify_regression(
        "inherited process configuration isolation", "cmd/server/process_test_helpers_test.go",
        'if !strings.HasPrefix(entry, "FBS_") {', 'if true {',
        "./cmd/server", "^TestServerProcessIgnoresInheritedStorageAndAuth$", "inherited",
    )
    verify_regression(
        "real in-flight shutdown drain", "cmd/server/serve.go",
        "srv.Shutdown(shutdownCtx)", "func() error { _ = shutdownCtx; return srv.Close() }()",
        "./cmd/server", "^TestServeShutdownWaitsForInflightRequest$", "serve returned with an in-flight request",
    )

    verify_regression(
        "bounded body reader", "internal/http/request_body.go",
        "http.MaxBytesReader(w, r.Body, limit)", "io.LimitReader(r.Body, limit)",
        "./internal/http", "^TestReadBodyEnforcesExactLimit$", "want MaxBytesError",
    )
    verify_regression(
        "single JSON document", "internal/http/request_body.go",
        "if err := decoder.Decode(&extra); err != io.EOF {",
        "if err := decoder.Decode(&extra); false && err != io.EOF {",
        "./internal/http", "^TestDecodeJSONBodyFramingAndOptions$", "wantErr=true",
    )
    verify_regression(
        "Bearer scheme detection", "internal/auth/bearer.go",
        'if len(parts) > 0 && !strings.EqualFold(parts[0], "Bearer") {',
        'if false && len(parts) > 0 && !strings.EqualFold(parts[0], "Bearer") {',
        "./internal/auth", "^TestChainPreservesSigV4Failure$", "chain error =",
    )
    verify_regression(
        "configured response write timeout", "internal/config/config.go",
        "WriteTimeout:             writeTimeout,", "WriteTimeout:             defaults.WriteTimeout,",
        "./internal/config", "^TestLoadWiresEnvironmentAndFlags$", "Load() =",
    )

    verify_regression(
        "configured shutdown deadline", "cmd/server/serve.go",
        "context.WithTimeout(context.Background(), cfg.ShutdownTimeout)",
        "context.WithTimeout(context.Background(), 1_000_000_000)",
        "./cmd/server", "^TestServeShutdownUsesConfiguredDeadline$",
        "serve ignored the configured shutdown deadline",
    )
    verify_regression(
        "premature shutdown deadline", "cmd/server/serve.go",
        "context.WithTimeout(context.Background(), cfg.ShutdownTimeout)",
        "context.WithTimeout(context.Background(), 0)",
        "./cmd/server", "^TestServeShutdownUsesConfiguredDeadline$", "shutdown elapsed =",
    )


if __name__ == "__main__":
    main()
