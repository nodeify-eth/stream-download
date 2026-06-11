package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nodeify-eth/stream-download/internal/source"
)

func TestRunRestoresSmallGzipTar(t *testing.T) {
	archive := gzipTar(t, "chaindata/file.txt", "hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"snap"`)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	scratch := filepath.Join(t.TempDir(), "scratch")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        scratch,
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "chaindata/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("restored file = %q", got)
	}
}

func TestRunAutoDetectsGzipFromURL(t *testing.T) {
	archive := gzipTar(t, "auto/file.txt", "gzip-auto")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL + "/snapshot.tar.gz",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "auto/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "gzip-auto" {
		t.Fatalf("restored file = %q", got)
	}
}

func TestRunUsesHTTPRangesWhenContentLengthKnown(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "range")
	var sawRange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"range-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		if r.Header.Get("Range") != "" {
			sawRange = true
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(archive)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"RANGE_SIZE":         "1MiB",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !sawRange {
		t.Fatalf("server did not observe ranged GET")
	}
	got, err := os.ReadFile(filepath.Join(dir, "db/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "range" {
		t.Fatalf("restored file = %q", got)
	}
}

func TestRunLogsRestoreProgress(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "progress")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"progress-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                filepath.Join(t.TempDir(), "data"),
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"RANGE_SIZE":         "1MiB",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, &stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"event":"restore_progress"`) {
		t.Fatalf("restore_progress event missing from output: %s", out)
	}
	if !strings.Contains(out, `"compressed_bytes_read"`) {
		t.Fatalf("compressed byte count missing from output: %s", out)
	}
	if !strings.Contains(out, `"bytes_per_second"`) {
		t.Fatalf("transfer rate missing from output: %s", out)
	}
	if !strings.Contains(out, `"eta_seconds"`) {
		t.Fatalf("ETA missing from output: %s", out)
	}
}

func TestRunFallsBackToSingleGETWithoutStrongHTTPIdentity(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "single-get")
	var sawRange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		if r.Header.Get("Range") != "" {
			sawRange = true
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"RANGE_SIZE":         "64",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if sawRange {
		t.Fatalf("server observed ranged GET without strong identity")
	}
}

func TestRunRejectsMissingRequiredChecksum(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "checksum-required")
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"REQUIRE_CHECKSUM":   "true",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err == nil {
		t.Fatalf("run succeeded without required checksum")
	}
	if requests != 0 {
		t.Fatalf("server saw %d requests before checksum validation, want 0", requests)
	}
}

func TestRunRejectsMismatchedChecksumBeforePublishingFiles(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "bad-checksum")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"checksum-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"CHECKSUM_SHA256":    strings.Repeat("0", 64),
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err == nil {
		t.Fatalf("run succeeded with mismatched checksum")
	}
	if _, err := os.Stat(filepath.Join(dir, "db/file.txt")); !os.IsNotExist(err) {
		t.Fatalf("restored file was published despite checksum mismatch: %v", err)
	}
}

func TestRunAcceptsMatchingChecksum(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "good-checksum")
	sum := sha256.Sum256(archive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"checksum-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"CHECKSUM_SHA256":    hex.EncodeToString(sum[:]),
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "db/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "good-checksum" {
		t.Fatalf("restored file = %q", got)
	}
}

func TestRunRestoresFromS3Source(t *testing.T) {
	archive := gzipTar(t, "s3/file.txt", "from-s3")
	original := newS3Source
	t.Cleanup(func() { newS3Source = original })
	newS3Source = func(ctx context.Context, bucket, key, endpoint string) (source.Reader, error) {
		if bucket != "snapshots" || key != "snapshot.tar.gz" || endpoint != "https://s3.example" {
			t.Fatalf("S3 source args = %q %q %q", bucket, key, endpoint)
		}
		return fakeSource{data: archive}, nil
	}

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"S3_ENDPOINT_URL":    "https://s3.example",
		"S3_BUCKET":          "snapshots",
		"S3_KEY":             "snapshot.tar.gz",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "s3/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "from-s3" {
		t.Fatalf("restored file = %q", got)
	}
}

func TestRunSkipsWhenCompletionStampMatches(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "skip")
	var getCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"skip-snap"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		if r.Method == http.MethodHead {
			return
		}
		getCount++
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("first run error: %v", err)
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if getCount != 1 {
		t.Fatalf("GET count = %d, want 1", getCount)
	}
}

type fakeSource struct {
	data []byte
}

func (f fakeSource) Resolve(context.Context) (source.Identity, error) {
	return source.Identity{Kind: "fake", URL: "fake://snapshot", Size: int64(len(f.data)), ETag: `"fake"`}, nil
}

func (f fakeSource) ReadRange(_ context.Context, r source.Range, _ source.Identity) (io.ReadCloser, source.Identity, error) {
	return io.NopCloser(bytes.NewReader(f.data[r.Start : r.End+1])), source.Identity{}, nil
}

func TestRunSplitsKnownObjectIntoMultipleRanges(t *testing.T) {
	want := strings.Repeat("range-data-", 200)
	archive := gzipTar(t, "db/big.txt", want)
	var rangeCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"split-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Fatalf("missing/invalid range header: %q", r.Header.Get("Range"))
		}
		rangeCount++
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(archive)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archive[start : end+1])
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":      "true",
		"DIR":                   dir,
		"SCRATCH_DIR":           filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":          srv.URL,
		"COMPRESSION":           "gzip",
		"RANGE_SIZE":            "64",
		"DOWNLOAD_CONCURRENCY":  "3",
		"DOWNLOAD_WINDOW_BYTES": "1MiB",
		"REQUIRE_MOUNTPOINT":    "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if rangeCount < 2 {
		t.Fatalf("rangeCount = %d, want multiple ranges", rangeCount)
	}
	got, err := os.ReadFile(filepath.Join(dir, "db/big.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != want {
		t.Fatalf("restored content mismatch")
	}
}

func gzipTar(t *testing.T, name, contents string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(contents))
	_ = tw.Close()
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write(raw.Bytes())
	_ = gw.Close()
	return gz.Bytes()
}
