# stream-download

`stream-download` restores large RPC node snapshots in Kubernetes without storing the full compressed archive on disk.

The tool is designed for initContainers. It resolves a snapshot source, downloads compressed bytes with bounded scratch usage, streams them through a decompressor, safely extracts tar entries into staging, and writes a completion stamp only after restore succeeds.

## Basic HTTP Restore

```bash
RESTORE_SNAPSHOT=true \
DIR=/data \
SCRATCH_DIR=/scratch \
SNAPSHOT_URL=https://example.com/snapshot.tar.zst \
stream-download
```

`COMPRESSION=auto` is the default and detects `.tar.gz`, `.tgz`, `.tar.zst`, `.tar.zstd`, `.tar.lz4`, `.tar.xz`, `.txz`, and `.tar`.

## S3-Compatible Restore

```bash
RESTORE_SNAPSHOT=true \
DIR=/data \
SCRATCH_DIR=/scratch \
S3_ENDPOINT_URL=https://s3.example.com \
S3_BUCKET=snapshots \
S3_KEY=base/snapshot.tar.zst \
stream-download
```

Credentials are loaded through the standard AWS SDK environment and web identity chain.

## Kubernetes Mounts

Mount the RPC data PVC at `/data` and a scratch volume at `/scratch`.

```yaml
volumeMounts:
  - name: rpc-data
    mountPath: /data
  - name: snapshot-scratch
    mountPath: /scratch
```

For multi-hundred-GiB or multi-TiB snapshots, prefer a scratch PVC. If using `emptyDir`, set pod and initContainer `ephemeral-storage` requests and limits above `DOWNLOAD_WINDOW_BYTES`.

Range downloads retry transient short reads and unexpected EOFs up to `MAX_RETRIES` before the restore fails. A pod restart starts extraction over from the compressed stream because the full archive is not kept on disk; stale staging from the failed attempt is cleaned automatically.

## Important Environment Variables

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
REQUIRE_CHECKSUM=false
ALLOW_WEAK_IDENTITY=false

DOWNLOAD_CONCURRENCY=8
DOWNLOAD_WINDOW_BYTES=8GiB
RANGE_SIZE=256MiB
MAX_EXTRACTED_BYTES=
MAX_EXTRACTED_FILES=
STRIP_COMPONENTS=0

COMPRESSION=auto
LOG_FORMAT=text
MAX_RETRIES=3
STALL_TIMEOUT=10m
WIPE_EXISTING=false
REQUIRE_MOUNTPOINT=true
PROGRESS_STATE_FILE=
```

## Safety

The extractor rejects absolute paths, `..` traversal, symlinks, hardlinks, device nodes, FIFOs, sockets, and setuid/setgid bits. It does not preserve archive owner or group by default.

Set `STRIP_COMPONENTS` to remove leading archive path components during extraction, equivalent to `tar --strip-components=N`.

By default, the target restore path must be empty. Set `WIPE_EXISTING=true` only when replacing an existing datadir is intentional.

The published container runs as UID/GID `1000:1000`. In Kubernetes, set volume ownership with `fsGroup: 1000` or an equivalent initContainer.

## Integrity

`CHECKSUM_SHA256` verifies the compressed archive byte stream. For multipart snapshots, the checksum applies to the concatenated part stream in extraction order.

Set `REQUIRE_CHECKSUM=true` for strict production environments. When enabled, startup fails before any network request unless `CHECKSUM_SHA256` is set.

## Logging

Text logging is the default so `kubectl logs -f` shows readable progress, speed, elapsed time, and ETA during long restores. Set `LOG_FORMAT=json` when shipping logs to structured collectors. Logs redact signed URL query parameters and authorization values.
