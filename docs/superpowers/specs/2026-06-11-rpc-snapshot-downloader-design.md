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
  -> safe tar extractor into staging
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

External downloaders are only allowed for sources that do not carry secrets in URLs, headers, argv, or input files. Credentialed S3 downloads must use the Go SDK path directly. Presigned URLs are treated as secret-bearing and must not be passed to child-process argv or written to world-readable files.

## Download Engines

### Multipart Snapshots

Multipart public HTTP snapshots use `aria2c` as the fetch engine. The tool starts a bounded readahead window of part downloads, emits completed parts to stdout in part-index order, and deletes each part immediately after it has been streamed.

Credentialed S3 multipart snapshots use the same ordered part model, but the fetch workers are implemented in Go through the S3 client instead of `aria2c`.

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

`aria2c` integration requirements:

- Invoke `aria2c` with a generated input file instead of placing URLs directly on argv.
- Create all `aria2c` input, control, and output files with mode `0600` inside `SCRATCH_DIR`.
- Map every URL to a deterministic part filename that includes only the part index.
- Treat `.aria2` control files as scratch state and delete them after success or retry cleanup.
- Redact all URLs from `aria2c` stderr before logging or writing termination summaries.
- Classify `aria2c` failures into retryable network errors, content errors, disk errors, and internal errors.

### Single-Object Snapshots

Single-object snapshots use custom HTTP/S3 range workers rather than forcing `aria2c` into a streaming role. Workers fetch byte ranges concurrently into scratch files. An ordered spooler streams range files to the decompressor in offset order and deletes each range after it is emitted.

This mode requires range support. If a source does not support ranges, the tool should fall back to a single ordered stream and log that parallelism is unavailable.

Ordered spooling invariants:

- Every chunk has immutable metadata: index, start offset, end offset, expected size, source identity, attempt, and scratch path.
- The spooler emits each byte offset exactly once and in ascending order.
- Gaps, duplicate chunks, short chunks, and unexpected `Content-Range` values abort the whole attempt.
- The final chunk size is derived from the pinned total object size.
- In-progress temp files count toward the scratch window.
- A downstream decompressor or extractor failure cancels all active workers.
- A failed chunk never advances the ordered stream.
- A chunk is deleted only after its bytes have been fully written downstream and the write returned successfully.

## Disk Model

The tool must never require space for both the full compressed archive and the extracted datadir.

It uses:

- Target datadir PVC mounted at `DIR`.
- Scratch directory mounted at `SCRATCH_DIR`.
- Staging extraction directory under the target volume.

Compressed scratch usage is bounded by `DOWNLOAD_WINDOW_BYTES` or the equivalent part-window settings. Extracted data lands in staging and is finalized only after the full restore succeeds.

Failed attempts leave no completed restore stamp. On the next run, stale staging and scratch state may be cleaned before a new attempt begins.

Target volume policy:

- By default, the configured restore path must be empty unless an existing completion stamp matches the requested snapshot identity.
- Replacing a non-empty target requires `WIPE_EXISTING=true`.
- With `WIPE_EXISTING=true`, destructive cleanup starts only after source resolution, identity pinning, config validation, and disk preflight succeed.
- Staging and final target must live on the same filesystem.
- Finalization is a same-filesystem rename of a fully extracted staging directory into the configured restore path.
- If a final target already exists at finalize time, the tool aborts unless it is the exact empty path prepared by the restore state machine.
- Stale staging and scratch cleanup must not follow symlinks and must only operate under canonicalized tool-owned paths.

Capacity policy:

- Preflight scratch free bytes against `DOWNLOAD_WINDOW_BYTES`.
- Preflight target free bytes and free inodes when the archive manifest provides extracted size or file count.
- Enforce `MAX_EXTRACTED_BYTES` and `MAX_EXTRACTED_FILES` during extraction when configured.
- Classify target or scratch `ENOSPC` and inode exhaustion as `disk-full` and do not retry without external state change.

## Integrity Policy

The default policy is balanced mode:

- If a checksum or manifest is configured, verify it before finalizing.
- If no checksum is configured, pin source identity before restore.
- Source identity includes size, ETag when available, and Last-Modified when available.
- If source identity changes during restore, abort with a content-rotated exit class.
- `REQUIRE_CHECKSUM=true` enables strict mode and rejects snapshots without a checksum.

The checksum applies to the compressed archive byte stream. For multipart snapshots, it applies to the concatenated part stream in extraction order.

Balanced mode detects source rotation, not publisher authenticity. The tool must log a loud warning when restoring without checksum verification. If size is unknown or both ETag and Last-Modified are unavailable, the tool must fail closed unless `ALLOW_WEAK_IDENTITY=true` is explicitly set.

Source identity rules:

- Single-object HTTP range responses must return `206` with a `Content-Range` that matches the pinned total size and requested byte range.
- Conditional requests should use pinned validators when available.
- Weak ETags and S3 multipart ETags are not treated as cryptographic integrity.
- S3 version IDs should be pinned when available.
- Multipart mode pins identity per part and records the ordered list of part identities.

## Kubernetes Behavior

The primary deployment form is an initContainer that runs before the RPC node container starts.

Required behavior:

- Refuse to restore into `/`.
- Require restoring into mounted volumes by default.
- Use a lock file to prevent concurrent restore into the same datadir.
- Use a completion stamp to skip already-restored snapshots.
- Extract into staging first, then finalize into the target path.
- Write concise failure summaries to `/dev/termination-log`.
- Return classified exit codes for operational triage.

Lock and stamp behavior:

- The lock must be an advisory filesystem lock held for the full restore process, not just a plain sentinel file.
- Lock metadata should include pid, hostname, tool version, target path, and start time for diagnostics.
- A completion stamp is valid only when its snapshot identity, checksum, source URL or S3 object, target path, compression mode, tool version, and finalized directory identity match the current request.
- Failed restores must never write the completion stamp.
- In-progress sentinels are diagnostic only and must not be used as correctness locks.

Termination behavior:

- SIGTERM and SIGINT cancel workers, terminate child process groups, stop extraction, write a redacted termination summary, and exit with a signal-aware exit class.
- The implementation should assume Kubernetes may kill the initContainer during rollouts or eviction.
- Whole-restore retries must start from clean staging state.
- Per-range and per-part retries may reuse safe scratch state only when chunk identity and byte counts still match.

Recommended mounts:

```yaml
volumeMounts:
  - name: rpc-data
    mountPath: /data
  - name: snapshot-scratch
    mountPath: /scratch
```

Scratch volume guidance:

- A scratch PVC is recommended for multi-hundred-GiB or multi-TiB snapshots.
- If `emptyDir` is used, set pod and initContainer `ephemeral-storage` requests and limits larger than `DOWNLOAD_WINDOW_BYTES`.
- Memory-backed `emptyDir` is not recommended for snapshot restore.

Recommended security context:

```yaml
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
```

Run as non-root where the target volume ownership allows it. If ownership changes are required, prefer preparing volume permissions outside this restore tool rather than running the downloader privileged.

## Logging And Observability

Logging is a first-class feature. JSON logs are the Kubernetes default; text logs are available for local debugging.

No logs may include credentials, signed URLs, secret headers, raw authorization values, or unredacted downloader stderr.

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
- Recent structured stderr or error details from downloader, decompressor, and extractor.

All stderr and termination-log output must pass through the same redaction layer before it is persisted or emitted.

Final success should include:

- Snapshot identity.
- Whether checksum was verified.
- Total compressed bytes streamed.
- Total restore time.
- Final datadir size when measurable.
- Restored target path.

The tool should also write a small progress state file, when configured, containing the latest phase, attempt, bytes downloaded, bytes streamed, scratch usage, ETA, and exit class. This gives operators a bounded Kubernetes-facing status surface without tailing the full log stream.

## Safe Extraction Policy

Archive extraction must be implemented as a safe extractor, not a blind `tar -x` over untrusted paths.

Required behavior:

- Reject absolute paths.
- Reject paths containing `..` after path cleaning.
- Reject entries whose final canonical destination escapes staging.
- Reject symlinks and hardlinks that point outside staging.
- Reject device nodes, FIFOs, and sockets.
- Clear setuid and setgid bits.
- Do not preserve archive owner or group by default.
- Bound extracted bytes and file count when configured.
- Treat any unsafe entry as `validation` failure and abort the whole restore.

Using Go's `archive/tar` with explicit path validation is preferred. External `tar` may be used only if equivalent safety checks are performed before extraction.

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
AWS_WEB_IDENTITY_TOKEN_FILE=

CHECKSUM_SHA256=
CHECKSUM_URL=
REQUIRE_CHECKSUM=false
ALLOW_WEAK_IDENTITY=false

DOWNLOAD_CONCURRENCY=8
DOWNLOAD_WINDOW_BYTES=8GiB
RANGE_SIZE=256MiB
MAX_EXTRACTED_BYTES=
MAX_EXTRACTED_FILES=

COMPRESSION=auto
LOG_FORMAT=json
MAX_RETRIES=3
STALL_TIMEOUT=10m
WIPE_EXISTING=false
REQUIRE_MOUNTPOINT=true
PROGRESS_STATE_FILE=
```

`SNAPSHOT_URLS` is used for explicit multipart URL lists. `SNAPSHOT_URL` may also point to a multipart base if the tool can enumerate parts.

Configuration validation rules:

- `RESTORE_SNAPSHOT=false` exits successfully without modifying the target.
- `DIR` and `SCRATCH_DIR` must be absolute canonical paths.
- `SUBPATH` must be relative, local, and must not contain `..`.
- `DIR/SUBPATH` and `SCRATCH_DIR` must not overlap unless the scratch directory is under a reserved tool-owned directory.
- `SNAPSHOT_URLS` takes precedence over `SNAPSHOT_URL` for multipart mode.
- S3 bucket/key configuration is mutually exclusive with HTTP URL configuration unless `SOURCE_TYPE=auto` can resolve one unambiguous source.
- Human byte units such as `8GiB` and `256MiB` must reject invalid, zero, and negative values.
- `DOWNLOAD_CONCURRENCY`, `MAX_RETRIES`, and timeout values must reject zero or negative values.

Credential guidance:

- Prefer SDK credential chains, Kubernetes workload identity, IRSA-style web identity, or mounted secret files.
- Static AWS environment variables are supported for compatibility, but they must never be forwarded to child processes that do not need them.
- Child process environments should be minimized before spawning `aria2c` or external decompressors.

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
- `interrupted`: restore was cancelled by SIGINT or SIGTERM.

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
- Unit tests for ordered spooling gaps, duplicates, short chunks, downstream write failure, and cancellation.
- Unit tests for scratch window enforcement.
- Unit tests for URL redaction and secret-free logging.
- Unit tests for checksum success and mismatch.
- Unit tests for source identity pinning and rotation detection.
- Unit tests for config precedence, path canonicalization, unit parsing, and invalid values.
- Unit tests for completion stamp identity matching.
- Integration tests with a local HTTP range server.
- Integration tests with local S3-compatible storage such as MinIO.
- End-to-end test restoring a small `.tar.zst` snapshot.
- End-to-end test restoring multipart snapshot parts.
- Failure tests for truncation, disk-full simulation, decompressor failure, and extractor failure.
- Failure tests for malicious tar fixtures: absolute paths, `..`, symlink escape, hardlink escape, device node, FIFO, socket, setuid, and setgid.
- Failure tests for `aria2c` partial files, redacted stderr, and cleanup.
- Failure tests for a range server lying about `Content-Range`.
- Failure tests for source rotation mid-restore.
- Failure tests for stale staging, stale scratch, failed restore without stamp, and idempotent skip with matching stamp.
- Kubernetes-oriented tests for SIGTERM cancellation and termination-log redaction.

## Version 1 Decisions

- The implementation language is Go.
- Manifest support is optional in v1; direct `CHECKSUM_SHA256` and `CHECKSUM_URL` are sufficient for the first release.
- Explicit `SNAPSHOT_URLS` is required for multipart mode in v1. Automatic part enumeration can be added after the core restore path is stable.
- Staging finalization replaces the configured restore path after successful extraction and validation. Merge behavior is out of scope for v1.
