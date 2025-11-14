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
MAX_RETRIES=${MAX_RETRIES:-5}

STAMP="${DIR}/._download.stamp"
STATE_FILE="${DIR}/._download.state"

if [[ -f "${STAMP}" && "$(cat "${STAMP}")" = "${URL}" ]]; then
  echo "Already restored, exiting"
  exit 0
else
  echo "Preparing to download ${URL}"
fi

# Remove existing subpath if requested and not resuming
if [[ -d "${DIR}/${SUBPATH}" && "${RM_SUBPATH}" = "true" && ! -f "${STATE_FILE}" ]]; then
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

# Get file size for progress tracking
FILESIZE="$(curl --silent --head "$URL" | grep -i Content-Length | awk '{print $2}' | tr --delete --complement '[:alnum:]')"
echo "Total file size: ${FILESIZE} bytes ($(numfmt --to=iec-i --suffix=B ${FILESIZE} 2>/dev/null || echo ${FILESIZE}))"

# Check if we're resuming
START_BYTE=0
if [[ -f "${STATE_FILE}" ]]; then
  START_BYTE=$(cat "${STATE_FILE}")
  echo "Resuming from byte ${START_BYTE} ($(numfmt --to=iec-i --suffix=B ${START_BYTE} 2>/dev/null || echo ${START_BYTE}))"
  PERCENT=$((START_BYTE * 100 / FILESIZE))
  echo "Already completed: ${PERCENT}%"
fi

mkdir -p "${DIR}/${SUBPATH}"

# Download and extract with retry logic
function stream_and_extract {
  local start_pos=$1
  local retries=0
  
  # Background status monitor
  function status_monitor {
    local start_time=$(date +%s)
    local last_size=0
    
    while sleep 30; do
      if [[ -d "${DIR}/${SUBPATH}" ]]; then
        local current_size=$(du -sb "${DIR}/${SUBPATH}" 2>/dev/null | awk '{print $1}')
        local current_time=$(date +%s)
        local elapsed=$((current_time - start_time))
        local speed=$((current_size / elapsed))
        
        if [[ $speed -gt 0 ]]; then
          local eta=$(( (FILESIZE - current_size) / speed ))
          echo "Status: Extracted $(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size) | Speed: $(numfmt --to=iec-i --suffix=B/s $speed 2>/dev/null || echo ${speed}B/s) | ETA: ${eta}s"
        else
          echo "Status: Extracted $(numfmt --to=iec-i --suffix=B $current_size 2>/dev/null || echo $current_size)"
        fi
      fi
    done
  }
  
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "Attempt $((retries + 1))/${MAX_RETRIES}: Starting download and extraction from byte ${start_pos}"
    
    # Start status monitor in background
    status_monitor &
    local monitor_pid=$!
    
    set +e
    if [[ $start_pos -eq 0 ]]; then
      # Initial download - no Range header, show progress with pv
      echo "Starting download with progress monitoring..."
      curl --fail --location --insecure \
        --connect-timeout 30 \
        --speed-limit 10240 --speed-time 60 \
        "$URL" 2>/dev/null | \
        pv -f -p -t -e -r -b -s ${FILESIZE} -i 1 2>&1 | \
        ${DECOMPRESS_CMD} | \
        tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} 2>/dev/null
      local exit_code=${PIPESTATUS[3]}
    else
      # Resume download - use Range header
      local remaining=$((FILESIZE - start_pos))
      echo "Resuming: downloading remaining ${remaining} bytes with progress monitoring..."
      curl --fail --location --insecure \
        --connect-timeout 30 \
        --speed-limit 10240 --speed-time 60 \
        --header "Range: bytes=${start_pos}-" \
        "$URL" 2>/dev/null | \
        pv -f -p -t -e -r -b -s ${remaining} -i 1 2>&1 | \
        ${DECOMPRESS_CMD} | \
        tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} 2>/dev/null
      local exit_code=${PIPESTATUS[3]}
    fi
    set -e
    
    # Stop status monitor
    kill $monitor_pid 2>/dev/null || true
    wait $monitor_pid 2>/dev/null || true
    
    if [[ $exit_code -eq 0 ]]; then
      echo "Download and extraction completed successfully"
      return 0
    fi
    
    echo "ERROR: Download/extraction failed with exit code ${exit_code}"
    
    # For resumable approach, we'd need to track bytes downloaded
    # But tar streaming doesn't allow easy resume, so we just retry
    retries=$((retries + 1))
    
    if [[ $retries -lt $MAX_RETRIES ]]; then
      echo "Waiting 5 seconds before retry..."
      sleep 5
    fi
  done
  
  echo "ERROR: Failed after ${MAX_RETRIES} attempts"
  return 1
}

# Main execution
if ! stream_and_extract $START_BYTE; then
  echo "FATAL: Could not complete download and extraction"
  exit 1
fi

echo "Recording completion stamp"
echo "${URL}" > "${STAMP}"

echo "Cleaning up"
rm -f "${STATE_FILE}"

echo "Snapshot restore completed successfully"
exit 0
