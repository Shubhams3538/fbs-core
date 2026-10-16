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


if __name__ == "__main__":
    main()
