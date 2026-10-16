# Configuration

Configuration is loaded from environment variables and then overridden by command-line flags. Durations use Go duration strings such as `30s`, `15m`, or `24h`. Byte-size settings accept plain byte counts and parser-supported size strings.

## Defaults

| Setting | Default |
| --- | --- |
| HTTP address | `127.0.0.1:9000` |
| SQLite database | `./fbs.db` |
| Data directory | `./data` |
| Metadata cache | `536870912` bytes |
| S3 read `Cache-Control` | `private, max-age=0, must-revalidate` |
| Public read default TTL | `1h` |
| Public read maximum TTL | `24h` |
| HTTP read timeout | `15s` |
| HTTP write timeout | `30s` |
| HTTP idle timeout | `60s` |
| Shutdown timeout | `10s` |
| Multipart TTL | `24h` |
| Multipart cleanup interval | `1h` |
| CORS allowed origins | `http://localhost:3000`, `http://127.0.0.1:3000`, `http://localhost:5173`, `http://127.0.0.1:5173` |

## Environment Variables And Flags

| Environment variable | CLI flag | Description |
| --- | --- | --- |
| `FBS_HTTP_ADDR` | `--http-addr` | HTTP listen address. |
| `FBS_DB_PATH` | `--db-path` | SQLite database path or SQLite `file:` URI. |
| `FBS_DATA_DIR` | `--data-dir` | Object data root directory. |
| `FBS_DEV` | `--dev` | Enables local-only auth bypass. |
| `FBS_PUBLIC_BASE_URL` | `--public-base-url` | External base URL used in setup responses and signed public URLs. |
| `FBS_METADATA_CACHE_SIZE` | `--metadata-cache-size` | Metadata cache byte budget. Set `0` to disable repository cache wrappers. |
| `FBS_S3_CACHE_CONTROL` | `--s3-cache-control` | `Cache-Control` header for authenticated S3 reads. |
| `FBS_PUBLIC_READ_SIGNING_SECRET` | `--public-read-signing-secret` | Enables signed public reads. Must be at least 32 bytes. |
| `FBS_PUBLIC_READ_DEFAULT_TTL` | `--public-read-default-ttl` | Default TTL for generated public object URLs. |
| `FBS_PUBLIC_READ_MAX_TTL` | `--public-read-max-ttl` | Maximum TTL allowed when generating public object URLs. |
| `FBS_CORS_ALLOWED_ORIGINS` | `--cors-allowed-origins` | Comma-separated allowed browser origins. |
| `FBS_READ_TIMEOUT` | `--read-timeout` | HTTP server read timeout. |
| `FBS_WRITE_TIMEOUT` | `--write-timeout` | Total HTTP response write deadline; socket progress timeout for object downloads. See transfer timeout policy below. |
| `FBS_IDLE_TIMEOUT` | `--idle-timeout` | HTTP server idle timeout. |
| `FBS_SHUTDOWN_TIMEOUT` | `--shutdown-timeout` | Graceful shutdown timeout. |
| `FBS_MULTIPART_TTL` | `--multipart-ttl` | Age after which active multipart uploads become stale. |
| `FBS_MULTIPART_CLEANUP_INTERVAL` | `--multipart-cleanup-interval` | Background cleanup interval. Minimum `1m`. |

## Validation Rules

The server refuses to start when:

- HTTP address, database path, or data directory is empty.
- Metadata cache size is negative.
- `public-base-url` is not a valid request URI.
- Public read signing secret is set but shorter than 32 bytes.
- Public read TTLs are non-positive or default TTL exceeds max TTL.
- CORS origin list is empty.
- HTTP or shutdown timeouts are non-positive.
- Multipart TTL is non-positive.
- Multipart cleanup interval is below `1m`.
- Dev mode is enabled while binding to anything other than `127.0.0.1`, `localhost`, or `::1`.

## Transfer Timeout Policy

`FBS_WRITE_TIMEOUT` defaults to `30s`. Ordinary responses retain the HTTP
server's total write deadline. Once an object has been authorized and opened,
the built-in HTTP server switches authenticated reads, signed public reads,
and share link reads to a socket progress timeout. This includes full GETs,
single ranges, and multipart range responses. Each socket write gets a fresh
deadline. If a write times out after sending some bytes, the server renews
the deadline and continues from the unsent bytes. If a timeout reports no
progress, the transfer ends and releases the handler. A partial write can
therefore take longer than the timeout without requiring a minimum rate for
completing a fixed-size chunk. Buffered progress can delay detection of a
stalled reader until a subsequent timeout reports no bytes sent.

Progress means bytes accepted by the server's socket, not bytes read by the
client application. TCP buffering and receive-window updates can pause
observable writes even while the client drains buffered data. Choose a timeout
longer than the expected network pauses; very small values can interrupt slow
clients. The default remains `30s`.

File transfers retain the standard library's optimized `ReadFrom`/`sendfile`
path where supported. Retried transfers preserve file offsets and range
limits, including when the operating system falls back to buffered copying.
HEAD and conditional responses retain `http.ServeContent` behavior. Requests
that fail authentication or object lookup retain the ordinary deadline. The
server resets the connection policy between requests, so keep-alive does not
carry a download's timeout behavior into later responses.

This policy is installed by the built-in HTTP/1 server's listener and request
connection context. Embedding the handlers in a different server does not
install it automatically. TLS and HTTP/2 termination at a reverse proxy is
independent of the backend connection's timeout policy.

`FBS_IDLE_TIMEOUT` controls idle keep-alive connections between requests; it
does not control an active download. Reverse proxies can impose their own
timeouts and buffering. Configure them to allow long transfers and preserve
range requests and object headers.

## CORS

CORS is configured globally with:

- Allowed methods: `GET`, `HEAD`, `POST`, `PATCH`, `PUT`, `DELETE`, `OPTIONS`.
- Allowed headers include `Authorization`, content headers, and common S3 SigV4 headers.
- Exposed headers: `ETag`, `x-amz-bucket-region`, and `Content-Disposition`.
- Credentials are allowed.
- Preflight max age is 300 seconds.


## Control Request Body Limits

Management and setup JSON requests accept at most 1 MiB and exactly one JSON
value, with trailing whitespace allowed within that limit. Bucket creation XML
is limited to 4 KiB. Multipart completion and multi-object deletion XML are
limited to 2 MiB. Oversized control requests return HTTP 400 using their existing
JSON or S3 error format. These limits apply to known-length and chunked bodies.
Object and part upload streams retain their existing streaming behavior.
