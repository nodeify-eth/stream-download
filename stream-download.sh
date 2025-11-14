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
CHUNK_SIZE=${CHUNK_SIZE:-$((1000 * 1000 * 1000))}  # 1GB chunks (default)
COMPRESSION=${COMPRESSION:-"auto"}
MAX_RETRIES=${MAX_RETRIES:-3}

WORK_DIR="${DIR}/._download"
TAR_FILE="${WORK_DIR}/archive.tar"
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

# Get file size
FILESIZE="$(curl --silent --head "$URL" | grep -i Content-Length | awk '{print $2}' | tr --delete --complement '[:alnum:]')"
NR_PARTS=$((FILESIZE / CHUNK_SIZE))
if ((FILESIZE % CHUNK_SIZE > 0)); then
  ((NR_PARTS++))
fi

echo "Total file size: ${FILESIZE} bytes ($(numfmt --to=iec-i --suffix=B ${FILESIZE} 2>/dev/null || echo ${FILESIZE}))"
echo "Will download in ${NR_PARTS} chunks of ~$(numfmt --to=iec-i --suffix=B ${CHUNK_SIZE} 2>/dev/null || echo ${CHUNK_SIZE})"

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

mkdir -p "${WORK_DIR}"
mkdir -p "${DIR}/${SUBPATH}"

# Download chunk with retry
function downloadChunk {
  local partNr=$1
  local startPos=$(( (partNr - 1) * CHUNK_SIZE ))
  local endPos=$((startPos + CHUNK_SIZE - 1))
  
  if [[ $endPos -ge $FILESIZE ]]; then
    endPos=$((FILESIZE - 1))
  fi
  
  local retries=0
  while [[ $retries -lt $MAX_RETRIES ]]; do
    echo "Downloading chunk ${partNr}/${NR_PARTS} (attempt $((retries + 1))/${MAX_RETRIES})"
    
    set +e
    curl --fail --silent --insecure \
      --output "${TAR_FILE}.part${partNr}" \
      --header "Range: bytes=${startPos}-${endPos}" \
      --connect-timeout 30 \
      --max-time 600 \
      "$URL"
    local exit_code=$?
    set -e
    
    if [[ $exit_code -eq 0 ]]; then
      local size=$(stat --format %s "${TAR_FILE}.part${partNr}")
      echo "Chunk ${partNr} downloaded successfully (${size} bytes)"
      return 0
    fi
    
    echo "Download failed for chunk ${partNr}, retrying..."
    rm -f "${TAR_FILE}.part${partNr}"
    retries=$((retries + 1))
    sleep 2
  done
  
  echo "ERROR: Failed to download chunk ${partNr}"
  return 1
}

# Check if we're resuming
START_CHUNK=1
if [[ -f "${STATE_FILE}" ]]; then
  START_CHUNK=$(cat "${STATE_FILE}")
  echo "Resuming from chunk ${START_CHUNK}/${NR_PARTS}"
fi

# Download all chunks
for partNr in $(seq $START_CHUNK $NR_PARTS); do
  if [[ -f "${TAR_FILE}.part${partNr}" ]]; then
    echo "Chunk ${partNr} already exists, skipping download"
  else
    if ! downloadChunk $partNr; then
      echo "FATAL: Could not download chunk ${partNr}"
      exit 1
    fi
  fi
  
  # Append chunk to tar file immediately and delete it
  echo "Appending chunk ${partNr}/${NR_PARTS} to tar file"
  cat "${TAR_FILE}.part${partNr}" >> "${TAR_FILE}"
  rm -f "${TAR_FILE}.part${partNr}"
  
  # Save progress
  echo "$((partNr + 1))" > "${STATE_FILE}"
  echo "Progress: ${partNr}/${NR_PARTS} chunks downloaded and appended"
done

echo "All chunks assembled into tar file ($(stat --format %s "${TAR_FILE}") bytes), extracting..."

# Extract the complete tar file (with decompression if needed)
${DECOMPRESS_CMD} < "${TAR_FILE}" | tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} 2>/dev/null || \
  ${DECOMPRESS_CMD} < "${TAR_FILE}" | tar --extract --ignore-zeros --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS}

echo "Extraction complete, cleaning up..."
rm -f "${TAR_FILE}"
rm -rf "${WORK_DIR}"
rm -f "${STATE_FILE}"

echo "Recording completion stamp"
echo "${URL}" > "${STAMP}"

echo "Snapshot restore completed successfully"
exit 0
