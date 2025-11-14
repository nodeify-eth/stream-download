#!/usr/bin/env bash
set -ex -o pipefail

RESTORE_SNAPSHOT=${RESTORE_SNAPSHOT:-"false"}
RESTORE_SNAPSHOT=${RESTORE_SNAPSHOT,,}

if [[ "${RESTORE_SNAPSHOT}" = "false" ]]; then
  echo "Skipping snapshot restore"
  exit 0
fi

if [[ -z "$URL" ]]; then
  echo "No URL to download set"
  exit 1
fi

if [[ -z "$DIR" ]]; then
  echo "No mount directory set, please set DIR env var"
  exit 1
fi

TAR_ARGS=${TAR_ARGS-""}
SUBPATH=${SUBPATH-""}
RM_SUBPATH=${RM_SUBPATH:-"true"}
RM_SUBPATH=${RM_SUBPATH,,}
COMPRESSION=${COMPRESSION:-"auto"}
MAX_RETRIES=${MAX_RETRIES:-10}  # More retries for production

STAMP="${DIR}/._download.stamp"

if [[ -f "${STAMP}" && "$(cat "${STAMP}")" = "${URL}" ]]; then
  echo "Already restored, exiting"
  exit 0
else
  echo "Preparing to download ${URL}"
fi

# Remove existing subpath if requested
if [[ -d "${DIR}/${SUBPATH}" && "${RM_SUBPATH}" = "true" ]]; then
  echo "Removing existing subpath: ${DIR}/${SUBPATH}"
  rm -rf "${DIR}/${SUBPATH}"
fi

# Auto-detect compression
if [[ "${COMPRESSION}" = "auto" ]]; then
  case "${URL}" in
    *.tar.zst|*.tar.zstd)
      COMPRESSION="zstd"
      echo "Auto-detected compression: zstd"
      ;;
    *.tar.lz4)
      COMPRESSION="lz4"
      echo "Auto-detected compression: lz4"
      ;;
    *.tar.gz|*.tgz)
      COMPRESSION="gzip"
      echo "Auto-detected compression: gzip"
      ;;
    *.tar.bz2|*.tbz2)
      COMPRESSION="bzip2"
      echo "Auto-detected compression: bzip2"
      ;;
    *.tar.xz|*.txz)
      COMPRESSION="xz"
      echo "Auto-detected compression: xz"
      ;;
    *.tar)
      COMPRESSION="none"
      echo "Auto-detected compression: none (plain tar)"
      ;;
    *)
      COMPRESSION="none"
      echo "Could not auto-detect compression, assuming plain tar"
      ;;
  esac
fi

# Set up decompression command
case "${COMPRESSION}" in
  zstd|zst)
    DECOMPRESS_CMD="zstd -dc"
    ;;
  lz4)
    DECOMPRESS_CMD="lz4 -dc"
    ;;
  gzip|gz)
    DECOMPRESS_CMD="gzip -dc"
    ;;
  bzip2|bz2)
    DECOMPRESS_CMD="bzip2 -dc"
    ;;
  xz)
    DECOMPRESS_CMD="xz -dc"
    ;;
  none)
    DECOMPRESS_CMD="cat"
    ;;
  *)
    echo "Unknown compression type: ${COMPRESSION}"
    exit 1
    ;;
esac

# Get file size for info
FILESIZE="$(curl --silent --head "$URL" | grep -i Content-Length | awk '{print $2}' | tr --delete --complement '[:alnum:]')"
echo "Total file size: ${FILESIZE} bytes ($(numfmt --to=iec-i --suffix=B ${FILESIZE} 2>/dev/null || echo ${FILESIZE}))"

mkdir -p "${DIR}/${SUBPATH}"

# Watchdog to detect stalls
function watchdog {
  local last_size=0
  local stall_count=0
  local max_stalls=3
  
  while sleep 60; do
    if [[ -d "${DIR}/${SUBPATH}" ]]; then
      local current_size=$(du -sb "${DIR}/${SUBPATH}" 2>/dev/null | awk '{print $1}')
      
      if [[ $current_size -eq $last_size ]]; then
        stall_count=$((stall_count + 1))
        echo "WARNING: No progress detected for ${stall_count} minute(s) ($(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size) extracted)"
        
        if [[ $stall_count -ge $max_stalls ]]; then
          echo "WATCHDOG: Detected stall for ${max_stalls} minutes, killing download to trigger retry"
          pkill -P $$ curl || true
          return 1
        fi
      else
        if [[ $stall_count -gt 0 ]]; then
          echo "Progress resumed ($(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size) extracted)"
        fi
        stall_count=0
      fi
      
      last_size=$current_size
    fi
  done
}

# Status monitor
function status_monitor {
  local start_time=$(date +%s)
  local last_size=0
  
  while sleep 30; do
    if [[ -d "${DIR}/${SUBPATH}" ]]; then
      local current_size=$(du -sb "${DIR}/${SUBPATH}" 2>/dev/null | awk '{print $1}')
      local current_time=$(date +%s)
      local elapsed=$((current_time - start_time))
      
      if [[ $elapsed -gt 0 && $current_size -gt 0 ]]; then
        local speed=$((current_size / elapsed))
        
        if [[ $speed -gt 0 ]]; then
          local remaining=$((FILESIZE - current_size))
          local eta=$((remaining / speed))
          local progress=$((current_size * 100 / FILESIZE))
          echo "Status: ${progress}% | $(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size) / $(numfmt --to=iec-i --suffix=B $FILESIZE 2>/dev/null || echo $FILESIZE) | Speed: $(numfmt --to=iec-i --suffix=B/s $speed 2>/dev/null || echo ${speed}B/s) | ETA: $((eta / 60))m"
        else
          echo "Status: $(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size) extracted"
        fi
      fi
    fi
  done
}

# Download and extract with robust retry logic
function stream_and_extract {
  local retries=0
  
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "=========================================="
    echo "Attempt $((retries + 1))/${MAX_RETRIES}: Starting download and extraction"
    echo "=========================================="
    
    # Start monitors in background
    status_monitor &
    local status_pid=$!
    
    watchdog &
    local watchdog_pid=$!
    
    set +e
    curl --fail --location --insecure \
      --connect-timeout 60 \
      --max-time 0 \
      --speed-limit 102400 \
      --speed-time 180 \
      --keepalive-time 60 \
      --tcp-nodelay \
      --compressed \
      "$URL" 2>/dev/null | \
      ${DECOMPRESS_CMD} | \
      tar --extract --ignore-zeros --warning=no-timestamp --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} 2>/dev/null
    
    local exit_code=$?
    set -e
    
    # Stop monitors
    kill $status_pid 2>/dev/null || true
    kill $watchdog_pid 2>/dev/null || true
    wait $status_pid 2>/dev/null || true
    wait $watchdog_pid 2>/dev/null || true
    
    if [[ $exit_code -eq 0 ]]; then
      echo "=========================================="
      echo "Download and extraction completed successfully!"
      echo "=========================================="
      return 0
    fi
    
    echo "=========================================="
    echo "ERROR: Download/extraction failed with exit code ${exit_code}"
    echo "=========================================="
    
    retries=$((retries + 1))
    
    if [[ $retries -lt $MAX_RETRIES ]]; then
      local wait_time=$((retries * 10))
      echo "Waiting ${wait_time} seconds before retry $((retries + 1))/${MAX_RETRIES}..."
      sleep $wait_time
    fi
  done
  
  echo "=========================================="
  echo "ERROR: Failed after ${MAX_RETRIES} attempts"
  echo "=========================================="
  return 1
}

# Main execution
if ! stream_and_extract; then
  echo "FATAL: Could not complete download and extraction after ${MAX_RETRIES} attempts"
  exit 1
fi

echo "Recording completion stamp"
echo "${URL}" > "${STAMP}"

echo "=========================================="
echo "Snapshot restore completed successfully"
echo "Final size: $(du -sh ${DIR}/${SUBPATH} 2>/dev/null || echo 'unknown')"
echo "=========================================="
exit 0
