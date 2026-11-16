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
	"time"

	"github.com/nodeify-eth/stream-download/internal/logx"
	"github.com/nodeify-eth/stream-download/internal/restore"
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

func TestRunRejectsMissingMountpointByDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT": "true",
		"DIR":              dir,
		"SCRATCH_DIR":      filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":     "https://example.invalid/snapshot.tar.gz",
		"COMPRESSION":      "gzip",
	}
	if err := run(env, io.Discard, io.Discard); err == nil {
		t.Fatalf("run succeeded without mountpoint")
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

func TestRunStripsPathComponents(t *testing.T) {
	archive := gzipTar(t, "a/b/c/chaindata/file.txt", "stripped")
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
		"STRIP_COMPONENTS":   "3",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "chaindata/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "stripped" {
		t.Fatalf("restored file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Fatalf("unstripped parent exists or stat failed: %v", err)
	}
}

func TestRunRemovesStaleStagingBeforeTargetCheck(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "restart")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dir, restore.StagingDirName, "partial"), 0755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL,
		"COMPRESSION":        "gzip",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "db/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "restart" {
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
		"LOG_FORMAT":         "json",
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
		"LOG_FORMAT":         "json",
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
}

func TestProgressReaderReportsRateAndETAAfterWarmup(t *testing.T) {
	var stdout bytes.Buffer
	logger := logx.New(&stdout, "json")
	p := &progressReader{
		logger:  logger,
		meta:    streamMetadata{sourceKind: "http", sourceSize: 10 * 1024 * 1024, rangeMode: true},
		started: time.Unix(0, 0),
		read:    2 * 1024 * 1024,
	}
	p.log(time.Unix(10, 0), false)
	out := stdout.String()
	if !strings.Contains(out, `"bytes_per_second"`) {
		t.Fatalf("transfer rate missing from output: %s", out)
	}
	if !strings.Contains(out, `"eta_seconds"`) {
		t.Fatalf("ETA missing from output: %s", out)
	}
}

func TestProgressReaderHumanMessage(t *testing.T) {
	p := &progressReader{
		meta: streamMetadata{sourceKind: "http", sourceSize: 10 * 1024 * 1024, rangeMode: true},
		read: 2 * 1024 * 1024,
	}
	msg := p.message(logx.Fields{
		"percent_complete": 20.0,
		"bytes_per_second": float64(1024 * 1024),
		"eta_seconds":      int64(8),
		"elapsed_seconds":  int64(2),
	}, false)
	for _, want := range []string{"20.0% downloaded", "1.00 MiB/s", "ETA 8s", "elapsed 2s", "2.00 MiB / 10.00 MiB"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
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

func TestRunWritesResolvedIdentityToStamp(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "identity")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"identity-snap"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
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
	if err := run(env, io.Discard, io.Discard); err != nil {
		t.Fatalf("run error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, restore.StampFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `etag:\"identity-snap\"`) {
		t.Fatalf("stamp missing identity: %s", data)
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
		"LOG_FORMAT":         "json",
	}
	if err := run(env, io.Discard, io.Discard); err != nil {
		t.Fatalf("first run error: %v", err)
	}
	var stdout bytes.Buffer
	if err := run(env, &stdout, io.Discard); err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if getCount != 1 {
		t.Fatalf("GET count = %d, want 1", getCount)
	}
	if !strings.Contains(stdout.String(), `"event":"restore_already_complete"`) {
		t.Fatalf("restore_already_complete missing: %s", stdout.String())
	}
}

func TestRunSkipsWhenStampPresentAndSourceReturns404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing snapshot", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, restore.StagingDirName, "partial")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	snapshotURL := srv.URL + "/snapshot.tar.zst?X-Amz-Signature=secret"
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  `etag:"prior-snap"`,
		Source:      snapshotURL,
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       snapshotURL,
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
		"LOG_FORMAT":         "json",
	}
	if err := run(env, &stdout, io.Discard); err != nil {
		t.Fatalf("run error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"level":"warn"`) || !strings.Contains(out, `"event":"restore_skipped_source_unavailable"`) {
		t.Fatalf("skip warning missing: %s", out)
	}
	if !strings.Contains(out, `prior-snap`) || !strings.Contains(out, "HEAD returned HTTP 404") || !strings.Contains(out, "snapshot.tar.zst") || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("skip log missing stamp identity, source, or resolve error: %s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("skip log leaked secret: %s", out)
	}
	if strings.Contains(out, `"event":"start_restore"`) {
		t.Fatalf("restore started: %s", out)
	}
	got, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging was touched: %v", err)
	}
}

func TestRunKeepsGETFallbackWhenHEADRejectsRequest(t *testing.T) {
	cases := []struct {
		name   string
		status int
		wipe   bool
	}{
		{name: "forbidden", status: http.StatusForbidden, wipe: true},
		{name: "method_not_allowed", status: http.StatusMethodNotAllowed, wipe: true},
		{name: "method_not_allowed_without_wipe", status: http.StatusMethodNotAllowed, wipe: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := gzipTar(t, "db/file.txt", "from-get")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write(archive)
			}))
			defer srv.Close()

			dir := filepath.Join(t.TempDir(), "data")
			old := filepath.Join(dir, "old.txt")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			snapshotURL := srv.URL + "/snapshot.tar.gz"
			if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
				SnapshotID:  `etag:"prior-snap"`,
				Source:      snapshotURL,
				Target:      dir,
				Compression: "gzip",
				ToolVersion: toolVersion,
			}); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"RESTORE_SNAPSHOT":   "true",
				"DIR":                dir,
				"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
				"SNAPSHOT_URL":       snapshotURL,
				"COMPRESSION":        "gzip",
				"REQUIRE_MOUNTPOINT": "false",
				"WIPE_EXISTING":      "false",
			}
			if tc.wipe {
				env["WIPE_EXISTING"] = "true"
			}
			err := run(env, io.Discard, io.Discard)
			if !tc.wipe {
				if err == nil || !strings.Contains(err.Error(), "WIPE_EXISTING=true") {
					t.Fatalf("run error = %v, want non-empty target error", err)
				}
				got, readErr := os.ReadFile(old)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != "old" {
					t.Fatalf("target changed: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("run error: %v", err)
			}
			got, readErr := os.ReadFile(filepath.Join(dir, "db/file.txt"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != "from-get" {
				t.Fatalf("restored file = %q", got)
			}
		})
	}
}

func TestRunDoesNotWipeWhenStampedSourceReturns404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing snapshot", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshotURL := srv.URL + "/snapshot.tar.zst"
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  `etag:"prior-snap"`,
		Source:      snapshotURL,
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	err := run(map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       snapshotURL,
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
		"WIPE_EXISTING":      "true",
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	got, readErr := os.ReadFile(payload)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunErrorsWhenInvalidStampAndSourceReturns404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing snapshot", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, restore.StampFileName), []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	err := run(map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL + "/snapshot.tar.zst",
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run succeeded, want error")
	}
	got, readErr := os.ReadFile(payload)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunErrorsWhenSourceReturns404WithoutStamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing snapshot", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL + "/snapshot.tar.zst",
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
	}
	err := run(env, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run succeeded, want error")
	}
	if strings.Contains(err.Error(), "restore_skipped_source_unavailable") {
		t.Fatalf("fresh restore skipped: %v", err)
	}
}

func TestRunAlreadyCompleteWhenExistingStampMatchesSource(t *testing.T) {
	var getCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"same-snap"`)
		w.Header().Set("Content-Length", "128")
		if r.Method == http.MethodHead {
			return
		}
		getCount++
		http.Error(w, "should not download", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshotURL := srv.URL + "/snapshot.tar.zst"
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  `etag:"same-snap"`,
		Source:      snapshotURL,
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       snapshotURL,
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
		"LOG_FORMAT":         "json",
	}
	if err := run(env, &stdout, io.Discard); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if getCount != 0 {
		t.Fatalf("GET count = %d, want 0", getCount)
	}
	if !strings.Contains(stdout.String(), `"event":"restore_already_complete"`) {
		t.Fatalf("restore_already_complete missing: %s", stdout.String())
	}
	got, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunKeepsWipeCheckWhenResolvedSnapshotDiffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"new-snap"`)
		w.Header().Set("Content-Length", "128")
		if r.Method != http.MethodHead {
			t.Errorf("unexpected %s before wipe check", r.Method)
		}
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  `etag:"prior-snap"`,
		Source:      srv.URL + "/snapshot.tar.zst",
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL + "/snapshot.tar.zst",
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
	}
	err := run(env, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "WIPE_EXISTING=true") {
		t.Fatalf("run error = %v, want non-empty target error", err)
	}
	got, readErr := os.ReadFile(payload)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunSkipsWhenStampPresentAndIdentityEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Errorf("unexpected %s", r.Method)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "chaindata")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  `etag:"prior-snap"`,
		Source:      srv.URL + "/snapshot.tar.zst",
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL":       srv.URL + "/snapshot.tar.zst",
		"COMPRESSION":        "zstd",
		"REQUIRE_MOUNTPOINT": "false",
		"LOG_FORMAT":         "json",
	}
	if err := run(env, &stdout, io.Discard); err != nil {
		t.Fatalf("run error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"event":"restore_skipped_source_unavailable"`) || !strings.Contains(out, "prior-snap") {
		t.Fatalf("skip log missing: %s", out)
	}
	if !strings.Contains(out, "snapshot identity is empty") {
		t.Fatalf("skip log missing empty identity: %s", out)
	}
	got, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunSkipsWhenStampPresentAndS3ResolveFails(t *testing.T) {
	original := newS3Source
	t.Cleanup(func() { newS3Source = original })
	newS3Source = func(ctx context.Context, bucket, key, endpoint string) (source.Reader, error) {
		return fakeSource{resolveErr: fmt.Errorf("HeadObject https://s3.example/snapshots/snapshot.tar.zst?X-Amz-Signature=secret: NotFound")}, nil
	}

	dir := filepath.Join(t.TempDir(), "data")
	payload := filepath.Join(dir, "bor", "chaindata")
	if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synced"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restore.WriteStamp(filepath.Join(dir, restore.StampFileName), restore.Stamp{
		SnapshotID:  "version:prior",
		Source:      "s3://snapshots/snapshot.tar.zst",
		Target:      dir,
		Compression: "zstd",
		ToolVersion: toolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                dir,
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"S3_ENDPOINT_URL":    "https://s3.example",
		"S3_BUCKET":          "snapshots",
		"S3_KEY":             "snapshot.tar.zst",
		"REQUIRE_MOUNTPOINT": "false",
		"LOG_FORMAT":         "json",
	}
	if err := run(env, &stdout, io.Discard); err != nil {
		t.Fatalf("run error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"level":"warn"`) || !strings.Contains(out, `"event":"restore_skipped_source_unavailable"`) || !strings.Contains(out, "version:prior") || !strings.Contains(out, "s3://snapshots/snapshot.tar.zst") {
		t.Fatalf("skip log missing: %s", out)
	}
	if strings.Contains(out, "secret") || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("resolve error was not redacted: %s", out)
	}
	got, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "synced" {
		t.Fatalf("target changed: %q", got)
	}
}

func TestRunErrorsWhenS3ResolveFailsWithoutStamp(t *testing.T) {
	original := newS3Source
	t.Cleanup(func() { newS3Source = original })
	newS3Source = func(ctx context.Context, bucket, key, endpoint string) (source.Reader, error) {
		return fakeSource{resolveErr: fmt.Errorf("HeadObject NotFound")}, nil
	}

	env := map[string]string{
		"RESTORE_SNAPSHOT":   "true",
		"DIR":                filepath.Join(t.TempDir(), "data"),
		"SCRATCH_DIR":        filepath.Join(t.TempDir(), "scratch"),
		"S3_ENDPOINT_URL":    "https://s3.example",
		"S3_BUCKET":          "snapshots",
		"S3_KEY":             "snapshot.tar.zst",
		"REQUIRE_MOUNTPOINT": "false",
	}
	err := run(env, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "NotFound") {
		t.Fatalf("run error = %v, want NotFound", err)
	}
}

type fakeSource struct {
	data       []byte
	resolveErr error
}

func (f fakeSource) Resolve(context.Context) (source.Identity, error) {
	if f.resolveErr != nil {
		return source.Identity{}, f.resolveErr
	}
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
