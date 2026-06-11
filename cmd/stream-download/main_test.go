package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
