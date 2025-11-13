# Init Stream Download - Enhanced

Enhanced version of the graphops init-stream-download container with support for:
- zstd (`.tar.zst`, `.tar.zstd`)
- lz4 (`.tar.lz4`)
- gzip (`.tar.gz`, `.tgz`)
- bzip2 (`.tar.bz2`, `.tbz2`)
- xz (`.tar.xz`, `.txz`)
- Plain tar (`.tar`)

## Features

- **Auto-detection**: Automatically detects compression format based on file extension
- **Chunked downloads**: Downloads large files in configurable chunks for reliability
- **Progress monitoring**: Shows download and extraction progress with `pv`
- **Skip re-download**: Won't re-download if snapshot already completed (via stamp file)
- **Multiple compression formats**: Supports all major compression formats

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RESTORE_SNAPSHOT` | `false` | Set to `true` to enable snapshot restore |
| `URL` | - | **Required**. URL of the snapshot to download |
| `DIR` | - | **Required**. Directory to extract snapshot to |
| `CHUNK_SIZE` | `1000000000` | Download chunk size in bytes (1GB default) |
| `COMPRESSION` | `auto` | Compression format: `auto`, `none`, `gzip`, `bzip2`, `xz`, `zstd`, `lz4` |
| `TAR_ARGS` | `""` | Additional arguments to pass to tar (e.g., `--strip-components=2`) |
| `SUBPATH` | `""` | Subdirectory within `DIR` to extract to (e.g., `/data`) |
| `RM_SUBPATH` | `"true"` | Remove `SUBPATH` directory before extraction (set to `"false"` to keep) |

## Usage

### Kubernetes StatefulSet Example (Optimism with zstd)

```yaml
initContainers:
  - name: init-download
    image: your-registry/init-stream-download:latest
    imagePullPolicy: IfNotPresent
    securityContext:
      privileged: true
    volumeMounts:
      - name: storage
        mountPath: /storage
    env:
      - name: DIR
        value: "/storage"
      - name: RESTORE_SNAPSHOT
        value: "true"
      - name: URL
        value: https://datadirs.optimism.io/mainnet-legacy-archival.tar.zst
      - name: CHUNK_SIZE
        value: "5000000000"  # 5GB chunks
      - name: COMPRESSION
        value: "auto"  # Will auto-detect .tar.zst
```

### Kubernetes StatefulSet Example (Scroll with plain tar)

```yaml
initContainers:
  - name: init-download
    image: your-registry/init-stream-download:latest
    imagePullPolicy: IfNotPresent
    securityContext:
      privileged: true
    volumeMounts:
      - name: storage
        mountPath: /storage
    env:
      - name: DIR
        value: "/storage"
      - name: RESTORE_SNAPSHOT
        value: "true"
      - name: URL
        value: https://scroll-geth-snapshot.s3.us-west-2.amazonaws.com/mpt/latest.tar
      - name: CHUNK_SIZE
        value: "5000000000"
      - name: TAR_ARGS
        value: "--strip-components=2"
      - name: SUBPATH
        value: "/data"
      - name: RM_SUBPATH
        value: "true"
```

### Helm Chart Example (with templating)

```yaml
initContainers:
  - name: init-download
    image: your-registry/init-stream-download:latest
    env:
      - name: RESTORE_SNAPSHOT
        value: "true"
      - name: URL
        value: {{ $values.restoreSnapshot.url }}
      - name: DIR
        value: "/storage"
      - name: CHUNK_SIZE
        value: {{ $values.restoreSnapshot.chunkSize | default "5000000000" | quote }}
      - name: SUBPATH
        value: {{ $values.restoreSnapshot.subpath | default "" }}
      - name: RM_SUBPATH
        value: {{ $values.restoreSnapshot.cleanSubpath | default "true" | quote }}
      - name: TAR_ARGS
        value: {{ $values.restoreSnapshot.tarArgs | default "" | quote }}
    volumeMounts:
      - name: storage
        mountPath: /storage
```

### Docker Run Example

```bash
docker run --rm \
  -v /path/to/storage:/storage \
  -e RESTORE_SNAPSHOT=true \
  -e URL=https://example.com/snapshot.tar.zst \
  -e DIR=/storage \
  -e CHUNK_SIZE=5000000000 \
  your-registry/init-stream-download:latest
```

## Building

```bash
docker build -t your-registry/init-stream-download:latest .
docker push your-registry/init-stream-download:latest
```

### Multi-arch Build (optional)

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t your-registry/init-stream-download:latest \
  --push .
```

## Compression Format Auto-Detection

The container automatically detects compression format based on file extension:

- `*.tar.zst`, `*.tar.zstd` → zstd
- `*.tar.lz4` → lz4
- `*.tar.gz`, `*.tgz` → gzip
- `*.tar.bz2`, `*.tbz2` → bzip2
- `*.tar.xz`, `*.txz` → xz
- `*.tar` → none (plain tar)

You can override auto-detection by setting the `COMPRESSION` environment variable explicitly.

## How It Works

1. Downloads the snapshot in configurable chunks
2. Streams chunks through a named pipe
3. Decompresses on-the-fly based on detected format
4. Extracts directly to the target directory
5. Records a stamp file to prevent re-downloading

## Credits

This project is based on the excellent [init-stream-download](https://github.com/graphops/docker-builds/tree/main) tool by [GraphOps](https://github.com/graphops). We've extended it to support additional compression formats while maintaining full backward compatibility with the original.

## Advantages Over Original

- **zstd support**: Can handle modern zstd-compressed snapshots
- **lz4 support**: Can handle lz4-compressed snapshots
- **Auto-detection**: No need to manually specify compression format
- **Better compatibility**: Works with more snapshot sources
