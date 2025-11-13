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
COMPRESSION=${COMPRESSION:-"auto"}  # auto, none, gzip, bzip2, xz, zstd, lz4

WORK_DIR="${DIR}/._download"
CHUNKS_DIR="${DIR}/._download/chunks"
STAMP="${DIR}/._download.stamp"
PIPE="${WORK_DIR}/stream_pipe"

if [[ -f "${STAMP}" && "$(cat "${STAMP}")" = "${URL}"  ]]; then
  echo "Already restored, exiting"
  exit 0
else
  echo "Preparing to download ${URL}"
fi

if [[ -d "${DIR}/${SUBPATH}" && "${RM_SUBPATH}" = "true" ]]; then
  rm -rf "${DIR}/${SUBPATH}/.*"
fi

# Auto-detect compression based on file extension if set to auto
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

# Set up decompression command based on compression type
case "${COMPRESSION}" in
  zstd|zst)
    DECOMPRESS_CMD="zstd -dc"
    echo "Using zstd decompression"
    ;;
  lz4)
    DECOMPRESS_CMD="lz4 -dc"
    echo "Using lz4 decompression"
    ;;
  gzip|gz)
    DECOMPRESS_CMD="gzip -dc"
    echo "Using gzip decompression"
    ;;
  bzip2|bz2)
    DECOMPRESS_CMD="bzip2 -dc"
    echo "Using bzip2 decompression"
    ;;
  xz)
    DECOMPRESS_CMD="xz -dc"
    echo "Using xz decompression"
    ;;
  none)
    DECOMPRESS_CMD="cat"
    echo "No decompression needed"
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

function init {
  if [ -d "$WORK_DIR" ]; then
    rm -rf "${WORK_DIR}"
  fi
  mkdir -p "${CHUNKS_DIR}"
  mkfifo "${PIPE}"
}

function processChunk {
  filename=$1
  cat "$CHUNKS_DIR/$filename" >&4
  rm "$CHUNKS_DIR/$filename" > /dev/null 2>&1
}

function watchStream {
  local processedLastPart="false"
  inotifywait --quiet --monitor --inotify --event moved_to --format "%f" "$CHUNKS_DIR" | until [[ "$processedLastPart" = "true" ]]
  do
    read filename
    processChunk "$filename"
    processedPart="$(echo "$filename" | cut --delimiter '.' --fields 3)"
    if [ "$processedPart" -eq "$NR_PARTS" ]; then
      processedLastPart="true"
      exec 4>&-
      kill -s USR1 $$
    fi
  done
}

function download {
  local startPos=0
  local partNr=0
  local partPath=$(mktemp --tmpdir="${WORK_DIR}" "snapshot-download-XXXXXXXXXXXXX.part")
  local finishedDownload="false"
  
  until [[ "$finishedDownload" = "true" ]];
  do
    set +e
    if wget --quiet --no-check-certificate -O - "$URL" --start-pos "${startPos}" | pv --quiet --stop-at-size --size "$CHUNK_SIZE" > "$partPath"; then
      finishedDownload="true"
    fi
    set -e
    
    local downloadedSize="$(stat --format %s "$partPath")"
    
    if [[ "$finishedDownload" = "true" || $(( downloadedSize == CHUNK_SIZE )) ]]; then
      partNr=$(( startPos / CHUNK_SIZE + 1))
      mv "$partPath" "$CHUNKS_DIR/snapshot-download.part.$partNr" > /dev/null
      startPos=$((startPos + CHUNK_SIZE))
    fi
  done
}

function triggerFinished {
  echo "Finished downloading, recording stamp"
  echo "${URL}" > "${STAMP}"
  kill $download_pid || true > /dev/null 2>&1
  kill $watch_pid || true  > /dev/null 2>&1
  kill $cat_pid || true  > /dev/null 2>&1
  echo "Cleaning up"
  rm -rf "${WORK_DIR}"
  exit 0
}

trap triggerFinished SIGUSR1

init

exec 3<>"$PIPE" 4>"$PIPE" 5<"$PIPE"
exec 3>&-

watchStream&
watch_pid=$!

download&
download_pid=$!

if [[ ! -d "${DIR}/${SUBPATH}" ]]; then
  mkdir -p "${DIR}/${SUBPATH}"
fi

# Use decompression command in the pipeline
cat <&5 | pv --force --size "$FILESIZE" --progress --eta --timer | ${DECOMPRESS_CMD} | tar --verbose --extract --file - --directory "${DIR}/${SUBPATH}" ${TAR_ARGS} &
cat_pid=$!

wait
