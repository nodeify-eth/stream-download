# RPC Snapshot Downloader Design

## Goal

Build a production-grade Dockerized downloader for RPC node snapshots. The tool must restore large compressed tar snapshots into a Kubernetes-mounted datadir without storing the full compressed archive on disk.

The primary runtime target is a Kubernetes initContainer. Bare-metal/systemd use should remain possible, but Kubernetes behavior drives the defaults.

## Core Pipeline

The restore path is an ordered streaming pipeline:

```text
source resolver
  -> bounded parallel downloader
  -> ordered byte stream
  -> optional checksum tee
  -> decompressor
  -> tar extract into staging
  -> validation
  -> atomic finalize
  -> completion stamp
```

The downloader may fetch bytes out of order internally, but the decompressor and tar extractor must only receive archive bytes in strict order.

## Supported Sources

Version 1 supports:

- Public HTTP/HTTPS URLs.
- S3-compatible object storage with credentials.
- Multipart snapshots such as `snapshot.tar.zst.part0000`, `snapshot.tar.zst.part0001`.
- Single-object snapshots such as `snapshot.tar.zst`.

S3 support must avoid exposing secrets in argv, logs, or process metadata. Credential handling should support standard AWS environment variables and configurable S3-compatible endpoints.

## Download Engines

### Multipart Snapshots

Multipart snapshots use `aria2c` as the fetch engine. The tool starts a bounded readahead window of part downloads, emits completed parts to stdout in part-index order, and deletes each part immediately after it has been streamed.

Example behavior:

```text
download part 0,1,2,3 in parallel
wait for part 0
stream part 0
delete part 0
start part 4
wait for part 1
stream part 1
delete part 1
```

This keeps the speed advantage of parallel downloads while bounding compressed scratch usage.

### Single-Object Snapshots

Single-object snapshots use custom HTTP/S3 range workers rather than forcing `aria2c` into a streaming role. Workers fetch byte ranges concurrently into scratch files. An ordered spooler streams range files to the decompressor in offset order and deletes each range after it is emitted.

This mode requires range support. If a source does not support ranges, the tool should fall back to a single ordered stream and log that parallelism is unavailable.

## Disk Model

The tool must never require space for both the full compressed archive and the extracted datadir.

It uses:

- Target datadir PVC mounted at `DIR`.
- Scratch directory mounted at `SCRATCH_DIR`.
- Staging extraction directory under the target volume.

Compressed scratch usage is bounded by `DOWNLOAD_WINDOW_BYTES` or the equivalent part-window settings. Extracted data lands in staging and is finalized only after the full restore succeeds.

Failed attempts leave no completed restore stamp. On the next run, stale staging and scratch state may be cleaned before a new attempt begins.

## Integrity Policy

The default policy is balanced mode:

- If a checksum or manifest is configured, verify it before finalizing.
- If no checksum is configured, pin source identity before restore.
- Source identity includes size, ETag when available, and Last-Modified when available.
- If source identity changes during restore, abort with a content-rotated exit class.
- `REQUIRE_CHECKSUM=true` enables strict mode and rejects snapshots without a checksum.

The tool must log a loud warning when restoring without checksum verification.

## Kubernetes Behavior

The primary deployment form is an initContainer that runs before the RPC node container starts.

Required behavior:

- Refuse to restore into `/`.
- Prefer restoring only into mounted volumes.
- Use a lock file to prevent concurrent restore into the same datadir.
- Use a completion stamp to skip already-restored snapshots.
- Extract into staging first, then finalize into the target path.
- Write concise failure summaries to `/dev/termination-log`.
- Return classified exit codes for operational triage.

Recommended mounts:

```yaml
volumeMounts:
  - name: rpc-data
    mountPath: /data
  - name: snapshot-scratch
    mountPath: /scratch
```

## Logging And Observability

Logging is a first-class feature. JSON logs are the Kubernetes default; text logs are available for local debugging.

No logs may include credentials, signed URLs, secret headers, or raw authorization values.

Core events:

- `start_restore`
- `source_resolved`
- `identity_pinned`
- `download_window_started`
- `chunk_download_started`
- `chunk_downloaded`
- `chunk_streamed`
- `chunk_deleted`
- `extract_progress`
- `checksum_verified`
- `finalize_started`
- `restore_complete`
- `restore_failed`

Every JSON log should use stable fields where relevant:

```json
{
  "ts": "2026-06-11T12:00:00Z",
  "level": "info",
  "event": "chunk_streamed",
  "attempt": 1,
  "source_kind": "http",
  "chunk_index": 42,
  "bytes_downloaded": 1073741824,
  "bytes_streamed": 1073741824,
  "total_bytes": 918552576000,
  "download_bps": 842000000,
  "extract_bps": 510000000,
  "scratch_bytes_used": 4294967296,
  "active_workers": 8,
  "eta_seconds": 1120
}
```

Periodic progress logs should include:

- Compressed bytes downloaded.
- Compressed bytes streamed.
- Extracted data size when measurable.
- Download throughput.
- Extraction throughput.
- ETA.
- Scratch usage.
- Active worker count.
- Current attempt.

Failures should include:

- `exit_class`.
- Component name.
- Attempt count.
- Retryability.
- Redacted source identity.
- Recent structured stderr from downloader, decompressor, and tar.

Final success should include:

- Snapshot identity.
- Whether checksum was verified.
- Total compressed bytes streamed.
- Total restore time.
- Final datadir size when measurable.
- Restored target path.

## Configuration

Initial environment contract:

```bash
RESTORE_SNAPSHOT=true
DIR=/data
SUBPATH=
SCRATCH_DIR=/scratch

SNAPSHOT_URL=https://example.com/snapshot.tar.zst
SNAPSHOT_URLS=
SOURCE_TYPE=auto

S3_ENDPOINT_URL=
S3_BUCKET=
S3_KEY=
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_SESSION_TOKEN=

CHECKSUM_SHA256=
CHECKSUM_URL=
REQUIRE_CHECKSUM=false

DOWNLOAD_CONCURRENCY=8
DOWNLOAD_WINDOW_BYTES=8GiB
RANGE_SIZE=256MiB

COMPRESSION=auto
LOG_FORMAT=json
MAX_RETRIES=3
STALL_TIMEOUT=10m
```

`SNAPSHOT_URLS` is used for explicit multipart URL lists. `SNAPSHOT_URL` may also point to a multipart base if the tool can enumerate parts.

## Exit Classes

The tool should return stable classified failures:

- `config`: invalid configuration.
- `deps`: missing runtime dependency.
- `state`: lock, staging, or stamp state failure.
- `disk-full`: scratch or target volume cannot fit required data.
- `checksum`: checksum mismatch.
- `content-rotated`: source identity changed during restore.
- `truncation`: bytes streamed do not match expected source size.
- `validation`: archive or decompression validation failed.
- `retries-exhausted`: transient failures exceeded retry budget.
- `oom-signal`: repeated signal death or memory-limit behavior.

The numeric exit-code mapping should be documented in the README before release.

## Implementation Recommendation

Implement the production version as a small Go CLI rather than a large Bash script.

Reasons:

- Concurrent range scheduling is easier to test.
- Ordered spooling needs clear state management.
- S3-compatible auth is safer through libraries than shell argv.
- Structured logging and redaction can be centralized.
- Unit tests can cover retries, ordering, source rotation, and scratch cleanup.

The existing Bash script remains useful as behavioral reference for staging, stamps, retries, decompressor/tar integration, and Kubernetes-oriented exit classes.

## Test Strategy

Required tests:

- Unit tests for ordered spooling with out-of-order chunk completion.
- Unit tests for scratch window enforcement.
- Unit tests for URL redaction and secret-free logging.
- Unit tests for checksum success and mismatch.
- Unit tests for source identity pinning and rotation detection.
- Integration tests with a local HTTP range server.
- Integration tests with local S3-compatible storage such as MinIO.
- End-to-end test restoring a small `.tar.zst` snapshot.
- End-to-end test restoring multipart snapshot parts.
- Failure tests for truncation, disk-full simulation, decompressor failure, and tar failure.

## Version 1 Decisions

- The implementation language is Go.
- Manifest support is optional in v1; direct `CHECKSUM_SHA256` and `CHECKSUM_URL` are sufficient for the first release.
- Explicit `SNAPSHOT_URLS` is required for multipart mode in v1. Automatic part enumeration can be added after the core restore path is stable.
- Staging finalization replaces the configured restore path after successful extraction and validation. Merge behavior is out of scope for v1.
