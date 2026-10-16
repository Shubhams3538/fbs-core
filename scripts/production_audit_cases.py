"""HTTP cases for request integrity and metadata mutation boundaries."""

import base64
import hashlib
import hmac
import json
import urllib.parse
import xml.etree.ElementTree as ET
from datetime import datetime, timezone


def sign_payload_request(address, method, path, payload, credentials, presigned=False):
    """Independent SigV4 signer for local payload-integrity control checks."""
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    day = stamp[:8]
    scope = day + "/us-east-1/s3/aws4_request"
    digest = hashlib.sha256(payload).hexdigest()
    query = {}
    headers = {}
    if presigned:
        query = {"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": credentials["access_key_id"] + "/" + scope,
                 "X-Amz-Date": stamp, "X-Amz-Expires": "300", "X-Amz-SignedHeaders": "host", "X-Amz-Content-SHA256": digest}
        signed_headers = "host"
        canonical_headers = "host:" + address + "\n"
    else:
        signed_headers = "host;x-amz-content-sha256;x-amz-date"
        headers = {"X-Amz-Content-SHA256": digest, "X-Amz-Date": stamp}
        canonical_headers = "host:" + address + "\nx-amz-content-sha256:" + digest + "\nx-amz-date:" + stamp + "\n"
    canonical_query = urllib.parse.urlencode(sorted(query.items()), quote_via=urllib.parse.quote)
    canonical = "\n".join([method, path, canonical_query, canonical_headers, signed_headers, digest])
    to_sign = "\n".join(["AWS4-HMAC-SHA256", stamp, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    signing_key = ("AWS4" + credentials["secret_key"]).encode()
    for value in [day, "us-east-1", "s3", "aws4_request"]:
        signing_key = hmac.new(signing_key, value.encode(), hashlib.sha256).digest()
    signature = hmac.new(signing_key, to_sign.encode(), hashlib.sha256).hexdigest()
    if presigned:
        query["X-Amz-Signature"] = signature
        path += "?" + urllib.parse.urlencode(sorted(query.items()), quote_via=urllib.parse.quote)
    else:
        headers["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + credentials["access_key_id"] + "/" + scope + ", SignedHeaders=" + signed_headers + ", Signature=" + signature
    return path, headers


def verify_xml_mutations(request, expect):
    malformed_bucket = b"<CreateBucketConfiguration/><Extra/>"
    expect("second XML root rejects bucket creation", request("PUT", "/malformed-bucket", malformed_bucket), 400)
    expect("malformed bucket was not created", request("HEAD", "/malformed-bucket"), 404)
    malformed_delete = b"<Delete><Object><Key>object</Key></Object></Delete>trailing garbage"
    delete_md5 = base64.b64encode(hashlib.md5(malformed_delete).digest()).decode()
    expect("trailing XML text rejects deletion", request("POST", "/proof-bucket?delete", malformed_delete, {"Content-MD5": delete_md5}), 400)
    expect("malformed deletion preserves bytes", request("GET", "/proof-bucket/object"), 200, b"original")
    upload_xml = expect("initiate multipart framing proof", request("POST", "/proof-bucket/object?uploads", b""), 200)
    upload_id = next(element.text for element in ET.fromstring(upload_xml).iter() if element.tag.rsplit("}", 1)[-1] == "UploadId")
    upload_path = "/proof-bucket/object?" + urllib.parse.urlencode({"uploadId": upload_id})
    part = b"replacement"
    expect("upload framing proof part", request("PUT", upload_path + "&partNumber=1", part), 200)
    part_etag = hashlib.md5(part).hexdigest()
    malformed_complete = ('<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"' + part_etag + '"</ETag></Part></CompleteMultipartUpload><Extra/>').encode()
    expect("second XML root rejects multipart completion", request("POST", upload_path, malformed_complete), 400)
    expect("malformed completion preserves original", request("GET", "/proof-bucket/object"), 200, b"original")
    expect("malformed completion preserves upload", request("GET", upload_path), 200)
    expect("bucket with pending upload rejects forced deletion", request("DELETE", "/api/management/buckets/proof-bucket"), 409)
    expect("rejected bucket deletion preserves bytes", request("GET", "/proof-bucket/object"), 200, b"original")
    expect("abort framing proof upload", request("DELETE", upload_path), 204)


def verify_listing_markers(request, expect):
    for page_key in ["page/dir/a", "page/dir/z", "page/next.txt"]:
        expect("write listing marker " + page_key, request("PUT", "/proof-bucket/" + page_key, b"page"), 200)
    for version, marker in [("1", "marker"), ("2", "start-after")]:
        query = {"prefix": "page/", "delimiter": "/", "max-keys": "1", marker: "page/dir/m"}
        if version == "2":
            query["list-type"] = "2"
        listing = expect("V" + version + " listing filters common prefix before marker", request("GET", "/proof-bucket?" + urllib.parse.urlencode(query)), 200)
        listing_root = ET.fromstring(listing)
        fields = [(element.tag.rsplit("}", 1)[-1], element.text) for element in listing_root.iter()]
        if ("Key", "page/next.txt") not in fields or any(name == "CommonPrefixes" for name, _ in fields) or ("IsTruncated", "false") not in fields:
            raise AssertionError("delimited listing returned a prefix before the marker")


def verify_signed_payloads(request, expect, address, credentials):
    signed_body = b'{"display_name":"signed member","role":"member"}'
    signed_path, signed_headers = sign_payload_request(address, "POST", "/api/management/keys", signed_body, credentials)
    signed_member = json.loads(expect("signed management body control", request("POST", signed_path, signed_body, signed_headers, credential=""), 201))
    if signed_member["key"]["role"] != "member":
        raise AssertionError("valid signed management request changed member role")
    tampered_body = b'{"display_name":"signed member","role":"admin"}'
    # Reject before any credential or user is created.
    expect("changed signed management body rejected", request("POST", signed_path, tampered_body, signed_headers, credential=""), 400)
    keys = json.loads(expect("read keys after signature rejection", request("GET", "/api/management/keys"), 200))
    if any(key["display_name"] == "signed member" and key["role"] == "admin" for key in keys["keys"]):
        raise AssertionError("changed signed management body created an admin")
    digest_path, digest_headers = sign_payload_request(address, "PUT", "/proof-bucket/signed-digest", b"approved", credentials, presigned=True)
    expect("presigned payload digest control", request("PUT", digest_path, b"approved", digest_headers, credential=""), 200)
    expect("changed presigned payload rejected", request("PUT", digest_path, b"changed!", digest_headers, credential=""), 400)
    expect("digest rejection preserves original", request("GET", "/proof-bucket/signed-digest"), 200, b"approved")
