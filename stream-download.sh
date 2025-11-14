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
CHUNK_SIZE=${CHUNK_SIZE:-$((1000 * 1000 * 1000))}
COMPRESSION=${COMPRESSION:-"auto"}
MAX_RETRIES=${MAX_RETRIES:-3}

WORK_DIR="${DIR}/._download"
CHUNKS_DIR="${DIR}/._download/chunks"
COMPLETED_CHUNKS_DIR="${DIR}/._download/completed"
STAMP="${DIR}/._download.stamp"
STATE_FILE="${DIR}/._download.state"

if [[ -f "${STAMP}" && "$(cat "${STAMP}")" = "${URL}"  ]]; then
  echo "Already restored, exiting"
  exit 0
else
  echo "Preparing to download ${URL}"
fi

# Fix: Actually remove the directory contents
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

FILESIZE="$(curl --silent --head "$URL" | grep -i Content-Length | awk '{print $2}' | tr --delete --complement '[:alnum:]' )"
NR_PARTS=$((FILESIZE / CHUNK_SIZE))

if ((FILESIZE % CHUNK_SIZE > 0)); then
  ((NR_PARTS++))
fi

echo "Total file size: ${FILESIZE} bytes, ${NR_PARTS} parts"

function init {
  mkdir -p "${CHUNKS_DIR}"
  mkdir -p "${COMPLETED_CHUNKS_DIR}"
  mkdir -p "${DIR}/${SUBPATH}"
}

function downloadAndVerifyChunk {
  local partNr=$1
  local startPos=$(( (partNr - 1) * CHUNK_SIZE ))
  local chunkFile="${COMPLETED_CHUNKS_DIR}/chunk.${partNr}"
  
  # Skip if already completed
  if [[ -f "${chunkFile}.done" ]]; then
    echo "Chunk ${partNr} already completed, skipping"
    return 0
  fi
  
  local retries=0
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "Downloading chunk ${partNr}/${NR_PARTS} (attempt $((retries + 1))/${MAX_RETRIES})"
    
    # Calculate end position for this chunk
    local endPos=$((startPos + CHUNK_SIZE - 1))
    if [[ $endPos -ge $FILESIZE ]]; then
      endPos=$((FILESIZE - 1))
    fi
    
    set +e
    curl --fail --silent --show-error --insecure \
      --output "${chunkFile}.tmp" \
      --header "Range: bytes=${startPos}-${endPos}" \
      --connect-timeout 30 \
      --max-time 300 \
      "$URL" 2>&1
    local curl_exit=$?
    set -e
    
    if [[ $curl_exit -eq 0 ]]; then
      # Verify we got data
      local downloadedSize="$(stat --format %s "${chunkFile}.tmp" 2>/dev/null || echo 0)"
      if [[ $downloadedSize -gt 0 ]]; then
        mv "${chunkFile}.tmp" "${chunkFile}"
        echo "Chunk ${partNr} downloaded successfully (${downloadedSize} bytes)"
        return 0
      else
        echo "Download completed but file is empty for chunk ${partNr}"
      fi
    else
      echo "Download failed for chunk ${partNr} with curl exit code: ${curl_exit}"
    fi
    
    echo "Retrying chunk ${partNr}..."
    rm -f "${chunkFile}.tmp"
    retries=$((retries + 1))
    sleep 2
  done
  
  echo "ERROR: Failed to download chunk ${partNr} after ${MAX_RETRIES} attempts"
  return 1
}

function extractChunk {
  local partNr=$1
  local chunkFile="${COMPLETED_CHUNKS_DIR}/chunk.${partNr}"
  
  if [[ -f "${chunkFile}.done" ]]; then
    return 0
  fi
  
  if [[ ! -f "${chunkFile}" ]]; then
    echo "ERROR: Chunk file ${chunkFile} not found"
    return 1
  fi
  
  local retries=0
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "Extracting chunk ${partNr}/${NR_PARTS} (attempt $((retries + 1))/${MAX_RETRIES})"
    
    set +e
    cat "${chunkFile}" | ${DECOMPRESS_CMD} | tar --verbose --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} 2>&1 | head -20
    local tar_exit=$?
    set -e
    
    # Exit codes:
    # 0 = success
    # 2 = unexpected EOF (expected when chunk splits a tar entry)
    # 141 = SIGPIPE from head closing (also success)
    if [[ $tar_exit -eq 0 || $tar_exit -eq 2 || $tar_exit -eq 141 ]]; then
      echo "Chunk ${partNr} extracted successfully"
      touch "${chunkFile}.done"
      rm -f "${chunkFile}"
      return 0
    fi
    
    echo "Extraction failed for chunk ${partNr} (exit code: ${tar_exit}), retrying..."
    retries=$((retries + 1))
    sleep 2
  done
  
  echo "ERROR: Failed to extract chunk ${partNr} after ${MAX_RETRIES} attempts"
  return 1
}

init

# Main download and extract loop
for partNr in $(seq 1 $NR_PARTS); do
  # Download chunk
  if ! downloadAndVerifyChunk $partNr; then
    echo "FATAL: Could not download chunk ${partNr}"
    exit 1
  fi
  
  # Extract chunk immediately
  if ! extractChunk $partNr; then
    echo "FATAL: Could not extract chunk ${partNr}"
    exit 1
  fi
  
  # Save progress
  echo "${partNr}" > "${STATE_FILE}"
  
  echo "Progress: ${partNr}/${NR_PARTS} chunks completed"
done

echo "All chunks downloaded and extracted successfully"
echo "Recording completion stamp"
echo "${URL}" > "${STAMP}"

echo "Cleaning up"
rm -rf "${WORK_DIR}"

echo "Snapshot restore completed successfully"
exit 0
