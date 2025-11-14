# Production Streaming Snapshot Restore

A bulletproof bash script for streaming large tar archives directly to disk with automatic retry logic, stall detection, and progress monitoring.

## Features

- ✅ **Streaming extraction** - Downloads and extracts simultaneously, no temporary files
- ✅ **Production-grade reliability** - 10 automatic retries with exponential backoff
- ✅ **Stall detection** - Watchdog automatically kills and retries stalled downloads
- ✅ **Progress monitoring** - Real-time status updates with speed, ETA, and percentage
- ✅ **Compression support** - Auto-detects and handles zstd, lz4, gzip, bzip2, xz, and plain tar
- ✅ **Minimal disk usage** - No temporary tar file, extracts on-the-fly
- ✅ **Connection resilience** - TCP keepalive, nodelay, and aggressive timeout handling

## Use Cases

Perfect for:
- Blockchain snapshot restoration (Ethereum, Cosmos, etc.)
- Large database backups
- CI/CD deployment of large archives
- Any scenario where disk space is limited but reliability is critical

## Requirements

- bash 4.0+
- curl
- tar
- Compression tools (zstd, lz4, gzip, bzip2, xz) - only needed if using compressed archives
- Standard Unix utilities: du, awk, grep, stat, numfmt

## Usage

### Basic Usage

```bash
export RESTORE_SNAPSHOT=true
export URL="https://example.com/snapshot.tar"
export DIR="/data"

./stream-download.sh
```

### Docker Usage

```dockerfile
FROM alpine:3.22

RUN apk add --no-cache \
  bash curl tar \
  zstd lz4 gzip bzip2 xz \
  coreutils dumb-init

COPY stream-download.sh /usr/local/bin/stream-download.sh
RUN chmod +x /usr/local/bin/stream-download.sh

ENTRYPOINT ["/usr/bin/dumb-init", "--"]
CMD ["/bin/bash", "-c", "/usr/local/bin/stream-download.sh"]
```

### Kubernetes Usage

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: snapshot-restore
spec:
  initContainers:
  - name: restore-snapshot
    image: your-image:latest
    env:
    - name: RESTORE_SNAPSHOT
      value: "true"
    - name: URL
      value: "https://snapshot.arbitrum.foundation/arb1/classic-archive.tar"
    - name: DIR
      value: "/storage"
    - name: SUBPATH
      value: "db"
    - name: TAR_ARGS
      value: "--strip-components=1"
    volumeMounts:
    - name: data
      mountPath: /storage
  containers:
  - name: main
    image: your-app:latest
    volumeMounts:
    - name: data
      mountPath: /storage
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: your-pvc
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RESTORE_SNAPSHOT` | `false` | Set to `true` to enable snapshot restore |
| `URL` | - | **Required.** URL of the snapshot to download |
| `DIR` | - | **Required.** Directory to extract snapshot to |
| `SUBPATH` | `""` | Subdirectory within `DIR` to extract to (e.g., `db`) |
| `TAR_ARGS` | `""` | Additional arguments to pass to tar (e.g., `--strip-components=1`) |
| `COMPRESSION` | `auto` | Compression format: `auto`, `none`, `gzip`, `bzip2`, `xz`, `zstd`, `lz4` |
| `RM_SUBPATH` | `true` | Remove `SUBPATH` directory before extraction (set to `false` to keep) |
| `MAX_RETRIES` | `10` | Number of retry attempts before giving up |

## How It Works

### Streaming Architecture

```
┌─────────┐    ┌──────────────┐    ┌─────┐    ┌────────────┐
│  curl   │───▶│ decompressor │───▶│ tar │───▶│ /storage/* │
└─────────┘    └──────────────┘    └─────┘    └────────────┘
     │              │                  │              │
     └──────────────┴──────────────────┴──────────────┘
                          │
                    ┌─────▼──────┐
                    │  monitors  │
                    │ watchdog + │
                    │   status   │
                    └────────────┘
```

1. **curl** streams data from URL with connection monitoring
2. **decompressor** (if needed) decompresses on-the-fly
3. **tar** extracts files directly to disk
4. **monitors** track progress and detect stalls

### Retry Logic

- **Automatic retry** - Up to 10 attempts with exponential backoff (10s, 20s, 30s...)
- **Stall detection** - Watchdog kills download if no progress for 3 minutes
- **Connection monitoring** - curl aborts if speed drops below 100KB/s for 3 minutes

### Progress Monitoring

Updates every 30 seconds showing:
```
Status: 45% | 278GiB / 613GiB | Speed: 245MiB/s | ETA: 23m
```

Warnings on stall:
```
WARNING: No progress detected for 1 minute(s) (278GiB extracted)
WARNING: No progress detected for 2 minute(s) (278GiB extracted)
WARNING: No progress detected for 3 minute(s) (278GiB extracted)
WATCHDOG: Detected stall for 3 minutes, killing download to trigger retry
```

## Examples

### Arbitrum Snapshot Restoration

```bash
export RESTORE_SNAPSHOT=true
export URL="https://snapshot.arbitrum.foundation/arb1/classic-archive.tar"
export DIR="/storage"
export SUBPATH="db"
export TAR_ARGS="--strip-components=1"

./stream-download.sh
```

### Compressed Snapshot with Custom Settings

```bash
export RESTORE_SNAPSHOT=true
export URL="https://example.com/snapshot.tar.zst"
export DIR="/data"
export COMPRESSION="zstd"  # or use "auto" to auto-detect
export MAX_RETRIES=5

./stream-download.sh
```

### Skip Existing Data

```bash
export RESTORE_SNAPSHOT=true
export URL="https://example.com/snapshot.tar"
export DIR="/data"
export SUBPATH="database"
export RM_SUBPATH="false"  # Don't delete existing data

./stream-download.sh
```

## Troubleshooting

### Download keeps failing

**Check connection stability:**
```bash
# Test download speed
curl -o /dev/null https://your-snapshot-url.tar

# Check if server supports HTTP keepalive
curl -I https://your-snapshot-url.tar | grep -i "keep-alive"
```

**Increase retry attempts:**
```bash
export MAX_RETRIES=20
```

### Stalls frequently

The watchdog detects stalls after 3 minutes of no progress. If your connection is very slow but stable:

**Option 1:** Accept longer extraction time - the watchdog will trigger retry
**Option 2:** Use the chunked download version instead (see below)

### Out of disk space

This script uses minimal space (extracts on-the-fly), but you need enough space for the extracted data.

**Check space:**
```bash
df -h /storage
```

**If you need resume capability and have extra space**, use the chunked download version instead.

## Limitations

### No Resume Capability

⚠️ **Important:** This streaming approach cannot resume from a specific byte position. If the download fails, it restarts from the beginning.

**Why?** Tar archives must be read sequentially. Jumping to a mid-point causes tar to see garbage data and fail to extract correctly.

**Mitigation:**
- 10 retry attempts
- Stall detection and auto-recovery
- Connection monitoring
- Most downloads succeed on first attempt with good internet

### Acceptance of Trade-offs

This streaming approach prioritizes:
- ✅ Minimal disk space usage
- ✅ Immediate file availability
- ✅ Simple, predictable behavior

The trade-off is that failed downloads restart from the beginning. However, with 10 retry attempts, stall detection, and connection monitoring, the vast majority of downloads complete successfully on the first attempt.

## Performance

### Typical Performance

| Snapshot Size | Network Speed | Extraction Time |
|---------------|---------------|-----------------|
| 100GB | 100Mbps | ~2.5 hours |
| 500GB | 100Mbps | ~12 hours |
| 1TB | 1Gbps | ~2.5 hours |

### Bottlenecks

- **Network** - Usually the limiting factor
- **Disk I/O** - Can bottleneck on slow disks (HDD vs SSD)
- **CPU** - Decompression (zstd, lz4) can be CPU-intensive

## Security Considerations

- Uses `--insecure` flag for curl (skips SSL verification)
- Consider using `--cacert` instead for production with proper SSL
- No authentication - assumes public snapshot URLs
- No integrity verification - consider adding checksums

## Advanced Configuration

### Custom curl Options

Edit the script to add curl options:

```bash
curl --fail --location \
  --cacert /path/to/ca-bundle.crt \  # Add SSL verification
  --speed-limit 102400 \
  --speed-time 180 \
  "$URL" | ...
```

### Custom Watchdog Timing

Edit stall detection threshold:

```bash
# In watchdog function
local max_stalls=5  # Wait 5 minutes instead of 3
```

### Disable Watchdog

Comment out watchdog in stream_and_extract function:

```bash
# watchdog &
# local watchdog_pid=$!
```

## Support

For issues, questions, or contributions, please refer to your internal documentation or contact your DevOps team.


## Credits

This project is based on the excellent [init-stream-download](https://github.com/graphops/docker-builds/tree/main) tool by [GraphOps](https://github.com/graphops). We've extended it to support additional compression formats while maintaining full backward compatibility with the original.
