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
BUFFER_SIZE=${BUFFER_SIZE:-$((100 * 1024 * 1024))}  # 100MB buffer for extraction
COMPRESSION=${COMPRESSION:-"auto"}
MAX_RETRIES=${MAX_RETRIES:-5}

WORK_DIR="${DIR}/._download"
STAMP="${DIR}/._download.stamp"
STATE_FILE="${DIR}/._download.state"
PROGRESS_FILE="${DIR}/._download.progress"

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

mkdir -p "${WORK_DIR}"
mkdir -p "${DIR}/${SUBPATH}"

# Progress monitoring function (runs in background)
function monitor_progress {
  local fifo=$1
  local last_size=0
  local last_time=$(date +%s)
  
  while [[ -p "${fifo}" ]]; do
    if [[ -f "${PROGRESS_FILE}" ]]; then
      local current_size=$(cat "${PROGRESS_FILE}")
      local current_time=$(date +%s)
      local elapsed=$((current_time - last_time))
      
      if [[ $elapsed -ge 10 ]]; then
        local downloaded=$((current_size - last_size))
        local speed=$((downloaded / elapsed))
        local percent=$((current_size * 100 / FILESIZE))
        local remaining=$((FILESIZE - current_size))
        local eta=$((remaining / speed))
        
        echo "Progress: ${percent}% | Downloaded: $(numfmt --to=iec-i --suffix=B ${current_size} 2>/dev/null || echo ${current_size}) / $(numfmt --to=iec-i --suffix=B ${FILESIZE} 2>/dev/null || echo ${FILESIZE}) | Speed: $(numfmt --to=iec-i --suffix=B/s ${speed} 2>/dev/null || echo ${speed} B/s) | ETA: ${eta}s"
        
        last_size=$current_size
        last_time=$current_time
      fi
    fi
    sleep 10
  done
}

# Download and extract with progress tracking
function stream_and_extract {
  local start_pos=$1
  local retries=0
  
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "Attempt $((retries + 1))/${MAX_RETRIES}: Starting download and extraction from byte ${start_pos}"
    
    # Create named pipe for progress monitoring
    local fifo="${WORK_DIR}/progress.fifo"
    rm -f "${fifo}"
    mkfifo "${fifo}"
    
    # Start progress monitor in background
    monitor_progress "${fifo}" &
    local monitor_pid=$!
    
    set +e
    # Stream with curl, track progress, decompress, and extract
    if [[ $start_pos -eq 0 ]]; then
      # Initial download - no Range header
      curl --fail --location --insecure \
        --connect-timeout 30 \
        --speed-limit 10240 --speed-time 60 \
        "$URL" 2>&1 | \
        tee >(
          # Track downloaded bytes
          dd bs=1M status=none | \
          awk -v start=$start_pos -v progress="${PROGRESS_FILE}" -v state="${STATE_FILE}" '
            BEGIN { total=start }
            {
              print $0
              total += length($0)
              if (NR % 100 == 0) {
                print total > progress
                print total > state
                close(progress)
                close(state)
              }
            }
            END {
              print total > progress
              print total > state
            }
          '
        ) | \
        ${DECOMPRESS_CMD} | \
        tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS}
    else
      # Resume download - use Range header
      curl --fail --location --insecure \
        --connect-timeout 30 \
        --speed-limit 10240 --speed-time 60 \
        --header "Range: bytes=${start_pos}-" \
        "$URL" 2>&1 | \
        tee >(
          # Track downloaded bytes
          awk -v start=$start_pos -v progress="${PROGRESS_FILE}" -v state="${STATE_FILE}" '
            BEGIN { total=start }
            {
              print $0
              total += length($0)
              if (NR % 100 == 0) {
                print total > progress
                print total > state
                close(progress)
                close(state)
              }
            }
            END {
              print total > progress
              print total > state
            }
          '
        ) | \
        ${DECOMPRESS_CMD} | \
        tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS}
    fi
    
    local exit_code=$?
    set -e
    
    # Clean up progress monitor
    rm -f "${fifo}"
    kill $monitor_pid 2>/dev/null || true
    wait $monitor_pid 2>/dev/null || true
    
    if [[ $exit_code -eq 0 ]]; then
      echo "Download and extraction completed successfully"
      return 0
    fi
    
    echo "ERROR: Download/extraction failed with exit code ${exit_code}"
    
    # Check if we made progress
    if [[ -f "${STATE_FILE}" ]]; then
      local new_pos=$(cat "${STATE_FILE}")
      if [[ $new_pos -gt $start_pos ]]; then
        echo "Made progress: ${start_pos} -> ${new_pos} bytes"
        start_pos=$new_pos
        echo "Will retry from byte ${start_pos}"
        retries=0  # Reset retry counter since we made progress
      else
        echo "No progress made, incrementing retry counter"
        retries=$((retries + 1))
      fi
    else
      retries=$((retries + 1))
    fi
    
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
rm -rf "${WORK_DIR}"
rm -f "${STATE_FILE}"
rm -f "${PROGRESS_FILE}"

echo "Snapshot restore completed successfully"
exit 0
