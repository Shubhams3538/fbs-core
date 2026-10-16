#!/usr/bin/env python3
"""Exercise the audited HTTP behaviors against an isolated compiled server."""

import argparse
import json
import os
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from production_audit_cases import verify_listing_markers, verify_signed_payloads, verify_xml_mutations

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path)
    args = parser.parse_args()
    results = []
    with tempfile.TemporaryDirectory(prefix="fbs-production-proof-") as directory:
        binary = args.binary.resolve() if args.binary else Path(directory) / "fbs"
        if not args.binary:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/server"], cwd=ROOT, check=True)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            address = f"127.0.0.1:{listener.getsockname()[1]}"
        env = {key: value for key, value in os.environ.items() if not key.startswith("FBS_")}
        env.update(FBS_HTTP_ADDR=address, FBS_SHUTDOWN_TIMEOUT="2s")
        with open(Path(directory) / "server.log", "wb") as log:
            process = subprocess.Popen([str(binary)], cwd=directory, env=env, stdout=log, stderr=log)
            token = ""

            def request(method, path, payload=None, headers=None, credential=None):
                merged = dict(headers or {})
                bearer = token if credential is None else credential
                if bearer:
                    merged["Authorization"] = "Bearer " + bearer
                req = urllib.request.Request("http://" + address + path, data=payload, headers=merged, method=method)
                try:
                    response = urllib.request.urlopen(req, timeout=5)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    return response.status, response.read()

            def expect(name, response, status, body=None):
                actual_status, actual_body = response
                if actual_status != status or (body is not None and actual_body != body):
                    # Credential-bearing setup/key responses are never printed.
                    raise AssertionError(f"{name}: status={actual_status}, expected={status}; body_matches={body is None or actual_body == body}")
                results.append({"check": name, "status": actual_status})
                return actual_body

            try:
                deadline = time.monotonic() + 10
                while True:
                    if process.poll() is not None:
                        raise RuntimeError("server exited before readiness")
                    try:
                        if request("GET", "/readyz")[0] == 200:
                            break
                    except (OSError, urllib.error.URLError):
                        pass
                    if time.monotonic() > deadline:
                        raise TimeoutError("isolated server did not become ready")
                    time.sleep(0.02)
                setup = json.loads(expect("first admin bootstrap", request("POST", "/api/setup/bootstrap", b"{}"), 201))
                token = setup["bearer_token"]
                expect("create bucket", request("PUT", "/proof-bucket", b""), 200)
                expect("store original", request("PUT", "/proof-bucket/object", b"original"), 200)
                expect("conditional PUT rejects overwrite", request("PUT", "/proof-bucket/object", b"replacement", {"If-None-Match": "*"}), 412)
                expect("rejected PUT preserves bytes", request("GET", "/proof-bucket/object"), 200, b"original")
                expect("store copy source", request("PUT", "/proof-bucket/source", b"source"), 200)
                expect("conditional copy rejects overwrite", request("PUT", "/proof-bucket/object", b"", {"If-None-Match": "*", "x-amz-copy-source": "/proof-bucket/source"}), 412)
                expect("rejected copy preserves bytes", request("GET", "/proof-bucket/object"), 200, b"original")
                expect("AWS CRC64-NVME vector", request("PUT", "/proof-bucket/checksum", b"123456789", {"x-amz-checksum-crc64nvme": "rosUhgp5mIg="}), 200)
                expect("checksum object bytes", request("GET", "/proof-bucket/checksum"), 200, b"123456789")
                key = urllib.parse.quote("é/file", safe="/")
                expect("Unicode object write", request("PUT", "/proof-bucket/" + key, b"unicode"), 200)
                listing = expect("Unicode delimiter listing", request("GET", "/proof-bucket?" + urllib.parse.urlencode({"prefix": "é/", "delimiter": "/"})), 200)
                if "é/file".encode() not in listing:
                    raise AssertionError("Unicode object missing from delimited listing")
                expect("second JSON document rejected", request("POST", "/api/management/keys", b'{"display_name":"proof"} {}'), 400)
                expect("oversized management body rejected", request("POST", "/api/management/keys", b'{"display_name":"' + b"x" * (1 << 20) + b'"}'), 400)
                expect("chunked oversized bucket body rejected", request("PUT", "/oversized-bucket", iter([b" " * 4096, b"<CreateBucketConfiguration/>"])), 400)
                verify_xml_mutations(request, expect)
                verify_listing_markers(request, expect)
                verify_signed_payloads(request, expect, address, setup["sigv4"])
                member = json.loads(expect("create active member", request("POST", "/api/management/keys", b'{"display_name":"proof member","role":"member"}'), 201))
                expect("active member authenticates", request("GET", "/api/management/grants/me", credential=member["bearer_token"]), 200)
                expect("deactivate member", request("PATCH", "/api/management/keys/" + member["key"]["id"], b'{"is_active":false}'), 200)
                expect("inactive member denied", request("GET", "/api/management/grants/me", credential=member["bearer_token"]), 403)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                if process.returncode != 0:
                    raise RuntimeError(f"server shutdown exit code: {process.returncode}")
    print(json.dumps({"passed": len(results), "checks": results}, indent=2))


if __name__ == "__main__":
    main()
