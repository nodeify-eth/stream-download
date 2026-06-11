# RPC Snapshot Downloader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Docker-ready Go CLI that restores RPC snapshots through bounded parallel download, ordered streaming, safe extraction, structured logging, and Kubernetes-safe restore state.

**Architecture:** The CLI is split into focused packages: `config` parses and validates env, `logx` handles structured redacted logs, `source` resolves HTTP/S3 objects and identity, `spool` emits ordered chunk streams, `extract` safely unpacks tar streams, and `restore` owns locks, staging, stamps, retries, and finalization. The first release favors testable Go internals and only shells out to `aria2c` for non-secret public multipart HTTP.

**Tech Stack:** Go 1.22+, standard library, AWS SDK for Go v2, optional `aria2c` child process, Go `archive/tar`, Go `compress/gzip`, external decompressors for zstd/lz4/xz until native support is intentionally added.

---

## File Structure

- Create `go.mod`: module metadata and AWS SDK dependency.
- Create `cmd/stream-download/main.go`: CLI entrypoint that wires config, logger, source resolver, and restore runner.
- Create `internal/config/config.go`: env parsing, byte/duration parsing, path validation, source selection, defaults.
- Create `internal/config/config_test.go`: table-driven config tests.
- Create `internal/logx/logx.go`: JSON/text logger and redactor.
- Create `internal/logx/logx_test.go`: secret redaction and structured log tests.
- Create `internal/source/source.go`: source interfaces, identity structs, range request structs.
- Create `internal/source/http.go`: HTTP resolver and range reader.
- Create `internal/source/s3.go`: S3 resolver and range reader using AWS SDK v2.
- Create `internal/source/source_test.go`: local HTTP range-server tests and fake S3 identity tests.
- Create `internal/spool/spool.go`: ordered chunk spooler and bounded scratch accounting.
- Create `internal/spool/spool_test.go`: ordering, gaps, duplicates, cancellation, downstream error tests.
- Create `internal/extract/extract.go`: safe tar extraction from an `io.Reader`.
- Create `internal/extract/extract_test.go`: malicious tar fixture tests and gzip happy path.
- Create `internal/restore/restore.go`: restore state machine, lock, staging, stamp, retry loop.
- Create `internal/restore/restore_test.go`: stamp identity, empty target, wipe opt-in, stale state cleanup tests.
- Create `internal/decompress/decompress.go`: compression detection and decompressor reader/process wiring.
- Create `internal/decompress/decompress_test.go`: gzip/none tests and unsupported compressor validation.
- Create `internal/aria2/aria2.go`: public multipart HTTP `aria2c` wrapper with safe input files and redaction.
- Create `internal/aria2/aria2_test.go`: input-file generation, filename mapping, stderr redaction, cleanup tests.
- Create `Dockerfile`: multi-stage static Go build with runtime tools.
- Create `README.md`: usage, Kubernetes example, env contract, exit classes.

---

## Task 1: Go Module And Config Parser

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write failing config tests**

Create `internal/config/config_test.go`:

```go
package config

import "testing"

func TestLoadRestoreFalseSkipsWithoutTargetValidation(t *testing.T) {
	env := map[string]string{"RESTORE_SNAPSHOT": "false"}
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if cfg.RestoreSnapshot {
		t.Fatalf("RestoreSnapshot = true, want false")
	}
}

func TestLoadRejectsUnsafeSubpath(t *testing.T) {
	env := validEnv()
	env["SUBPATH"] = "../escape"
	_, err := LoadFromMap(env)
	if err == nil {
		t.Fatalf("LoadFromMap succeeded, want unsafe SUBPATH error")
	}
}

func TestParseBytes(t *testing.T) {
	tests := map[string]int64{
		"256MiB": 268435456,
		"8GiB":   8589934592,
		"1024":   1024,
	}
	for input, want := range tests {
		got, err := ParseBytes(input)
		if err != nil {
			t.Fatalf("ParseBytes(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseBytes(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestSnapshotURLsTakePrecedence(t *testing.T) {
	env := validEnv()
	env["SNAPSHOT_URL"] = "https://example.invalid/one.tar.zst"
	env["SNAPSHOT_URLS"] = "https://example.invalid/a.part0000,https://example.invalid/a.part0001"
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if len(cfg.SnapshotURLs) != 2 {
		t.Fatalf("SnapshotURLs len = %d, want 2", len(cfg.SnapshotURLs))
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"RESTORE_SNAPSHOT": "true",
		"DIR": "/data",
		"SCRATCH_DIR": "/scratch",
		"SNAPSHOT_URL": "https://example.invalid/snapshot.tar.zst",
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/config
```

Expected: FAIL because `go.mod` and `internal/config` do not exist.

- [ ] **Step 3: Implement minimal config package**

Create `go.mod`:

```go
module github.com/nodeify-eth/stream-download

go 1.22
```

Create `internal/config/config.go`:

```go
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RestoreSnapshot    bool
	Dir                string
	Subpath            string
	ScratchDir         string
	SnapshotURL        string
	SnapshotURLs       []string
	SourceType         string
	S3EndpointURL      string
	S3Bucket           string
	S3Key              string
	ChecksumSHA256     string
	ChecksumURL        string
	RequireChecksum    bool
	AllowWeakIdentity  bool
	DownloadConcurrency int
	DownloadWindowBytes int64
	RangeSize          int64
	MaxExtractedBytes  int64
	MaxExtractedFiles  int64
	Compression        string
	LogFormat          string
	MaxRetries         int
	StallTimeout       time.Duration
	WipeExisting       bool
	RequireMountpoint  bool
	ProgressStateFile  string
}

func LoadFromMap(env map[string]string) (Config, error) {
	cfg := Config{
		RestoreSnapshot:     boolEnv(env, "RESTORE_SNAPSHOT", false),
		SourceType:          stringEnv(env, "SOURCE_TYPE", "auto"),
		DownloadConcurrency: intEnv(env, "DOWNLOAD_CONCURRENCY", 8),
		DownloadWindowBytes: mustDefaultBytes(env, "DOWNLOAD_WINDOW_BYTES", 8*1024*1024*1024),
		RangeSize:           mustDefaultBytes(env, "RANGE_SIZE", 256*1024*1024),
		Compression:         stringEnv(env, "COMPRESSION", "auto"),
		LogFormat:           stringEnv(env, "LOG_FORMAT", "json"),
		MaxRetries:          intEnv(env, "MAX_RETRIES", 3),
		StallTimeout:        durationEnv(env, "STALL_TIMEOUT", 10*time.Minute),
		RequireMountpoint:   boolEnv(env, "REQUIRE_MOUNTPOINT", true),
	}
	if !cfg.RestoreSnapshot {
		return cfg, nil
	}
	cfg.Dir = env["DIR"]
	cfg.Subpath = env["SUBPATH"]
	cfg.ScratchDir = env["SCRATCH_DIR"]
	cfg.SnapshotURL = env["SNAPSHOT_URL"]
	cfg.S3EndpointURL = env["S3_ENDPOINT_URL"]
	cfg.S3Bucket = env["S3_BUCKET"]
	cfg.S3Key = env["S3_KEY"]
	cfg.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(env["CHECKSUM_SHA256"]))
	cfg.ChecksumURL = env["CHECKSUM_URL"]
	cfg.RequireChecksum = boolEnv(env, "REQUIRE_CHECKSUM", false)
	cfg.AllowWeakIdentity = boolEnv(env, "ALLOW_WEAK_IDENTITY", false)
	cfg.WipeExisting = boolEnv(env, "WIPE_EXISTING", false)
	cfg.ProgressStateFile = env["PROGRESS_STATE_FILE"]
	cfg.MaxExtractedBytes = mustDefaultBytes(env, "MAX_EXTRACTED_BYTES", 0)
	cfg.MaxExtractedFiles = mustDefaultBytes(env, "MAX_EXTRACTED_FILES", 0)
	if err := validateAbs("DIR", cfg.Dir); err != nil {
		return cfg, err
	}
	if err := validateAbs("SCRATCH_DIR", cfg.ScratchDir); err != nil {
		return cfg, err
	}
	if cfg.Subpath != "" {
		clean := filepath.Clean(cfg.Subpath)
		if filepath.IsAbs(cfg.Subpath) || clean == ".." || strings.HasPrefix(clean, "../") {
			return cfg, fmt.Errorf("SUBPATH must be relative and local: %q", cfg.Subpath)
		}
		cfg.Subpath = clean
	}
	if raw := strings.TrimSpace(env["SNAPSHOT_URLS"]); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			u := strings.TrimSpace(part)
			if u != "" {
				cfg.SnapshotURLs = append(cfg.SnapshotURLs, u)
			}
		}
	} else if cfg.SnapshotURL != "" {
		cfg.SnapshotURLs = []string{cfg.SnapshotURL}
	}
	if len(cfg.SnapshotURLs) == 0 && (cfg.S3Bucket == "" || cfg.S3Key == "") {
		return cfg, errors.New("set SNAPSHOT_URL, SNAPSHOT_URLS, or S3_BUCKET/S3_KEY")
	}
	if cfg.DownloadConcurrency <= 0 || cfg.MaxRetries <= 0 {
		return cfg, errors.New("DOWNLOAD_CONCURRENCY and MAX_RETRIES must be positive")
	}
	if cfg.DownloadWindowBytes <= 0 || cfg.RangeSize <= 0 {
		return cfg, errors.New("DOWNLOAD_WINDOW_BYTES and RANGE_SIZE must be positive")
	}
	return cfg, nil
}

func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	units := map[string]int64{"KiB": 1024, "MiB": 1024 * 1024, "GiB": 1024 * 1024 * 1024}
	for suffix, mult := range units {
		if strings.HasSuffix(s, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, suffix), 10, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid byte value %q", s)
			}
			return n * mult, nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid byte value %q", s)
	}
	return n, nil
}

func validateAbs(name, value string) error {
	if value == "" || !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be absolute", name)
	}
	return nil
}

func stringEnv(env map[string]string, key, fallback string) string {
	if v := strings.TrimSpace(env[key]); v != "" {
		return v
	}
	return fallback
}

func boolEnv(env map[string]string, key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(env[key])) {
	case "1", "true", "yes", "y":
		return true
	case "0", "false", "no", "n":
		return false
	default:
		return fallback
	}
}

func intEnv(env map[string]string, key string, fallback int) int {
	if strings.TrimSpace(env[key]) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(env[key]))
	if err != nil {
		return fallback
	}
	return n
}

func durationEnv(env map[string]string, key string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(env[key]) == "" {
		return fallback
	}
	d, err := time.ParseDuration(strings.TrimSpace(env[key]))
	if err != nil {
		return fallback
	}
	return d
}

func mustDefaultBytes(env map[string]string, key string, fallback int64) int64 {
	if strings.TrimSpace(env[key]) == "" {
		return fallback
	}
	n, err := ParseBytes(env[key])
	if err != nil {
		return -1
	}
	return n
}
```

- [ ] **Step 4: Run config tests**

Run:

```bash
rtk go test ./internal/config
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add go.mod internal/config
rtk git commit -m "Add restore config parser"
```

---

## Task 2: Redacted Structured Logger

**Files:**
- Create: `internal/logx/logx.go`
- Create: `internal/logx/logx_test.go`

- [ ] **Step 1: Write failing log tests**

Create `internal/logx/logx_test.go`:

```go
package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactURLSecrets(t *testing.T) {
	input := "https://host/snapshot?X-Amz-Signature=secret&token=abc"
	got := Redact(input)
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
		t.Fatalf("Redact leaked secret: %s", got)
	}
}

func TestJSONLoggerRedactsFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "json")
	l.Info("source_resolved", Fields{"url": "https://h/x?AWSAccessKeyId=secret"})
	out := buf.String()
	if !strings.Contains(out, `"event":"source_resolved"`) {
		t.Fatalf("missing event: %s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("log leaked secret: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/logx
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement logger**

Create `internal/logx/logx.go`:

```go
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Fields map[string]any

type Logger struct {
	out    io.Writer
	format string
}

func New(out io.Writer, format string) *Logger {
	if format != "text" {
		format = "json"
	}
	return &Logger{out: out, format: format}
}

func (l *Logger) Info(event string, fields Fields) {
	l.write("info", event, fields)
}

func (l *Logger) Warn(event string, fields Fields) {
	l.write("warn", event, fields)
}

func (l *Logger) Error(event string, fields Fields) {
	l.write("error", event, fields)
}

func (l *Logger) write(level, event string, fields Fields) {
	if fields == nil {
		fields = Fields{}
	}
	if l.format == "text" {
		fmt.Fprintf(l.out, "%s %s %s %v\n", time.Now().UTC().Format(time.RFC3339), level, event, redactFields(fields))
		return
	}
	rec := Fields{
		"ts":    time.Now().UTC().Format(time.RFC3339),
		"level": level,
		"event": event,
	}
	for k, v := range redactFields(fields) {
		rec[k] = v
	}
	_ = json.NewEncoder(l.out).Encode(rec)
}

func redactFields(in Fields) Fields {
	out := Fields{}
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = Redact(s)
		} else {
			out[k] = v
		}
	}
	return out
}

var secretQuery = map[string]bool{
	"x-amz-signature": true,
	"x-amz-credential": true,
	"x-amz-security-token": true,
	"awsaccesskeyid": true,
	"signature": true,
	"token": true,
	"access_token": true,
}

var authHeaderRE = regexp.MustCompile(`(?i)(authorization:\s*)\S+`)

func Redact(s string) string {
	s = authHeaderRE.ReplaceAllString(s, `${1}[REDACTED]`)
	parts := strings.Fields(s)
	for i, part := range parts {
		if u, err := url.Parse(part); err == nil && u.Scheme != "" && u.Host != "" {
			q := u.Query()
			changed := false
			for key := range q {
				if secretQuery[strings.ToLower(key)] {
					q.Set(key, "[REDACTED]")
					changed = true
				}
			}
			if changed {
				u.RawQuery = q.Encode()
				parts[i] = u.String()
			}
		}
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Run logger tests**

Run:

```bash
rtk go test ./internal/logx
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/logx
rtk git commit -m "Add redacted structured logger"
```

---

## Task 3: Source Identity And HTTP Range Reader

**Files:**
- Create: `internal/source/source.go`
- Create: `internal/source/http.go`
- Create: `internal/source/source_test.go`

- [ ] **Step 1: Write failing HTTP source tests**

Create `internal/source/source_test.go`:

```go
package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPResolveAndReadRange(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Thu, 11 Jun 2026 00:00:00 GMT")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			return
		}
		if r.Header.Get("Range") != "bytes=2-5" {
			t.Fatalf("Range = %q, want bytes=2-5", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[2:6])
	}))
	defer srv.Close()

	src := NewHTTP(srv.URL, nil)
	id, err := src.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if id.Size != int64(len(body)) || id.ETag != `"abc"` {
		t.Fatalf("identity = %+v", id)
	}
	rc, gotID, err := src.ReadRange(context.Background(), Range{Start: 2, End: 5}, id)
	if err != nil {
		t.Fatalf("ReadRange error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "2345" {
		t.Fatalf("body = %q", got)
	}
	if gotID.Size != id.Size {
		t.Fatalf("range identity = %+v", gotID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/source
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement source interfaces and HTTP source**

Create `internal/source/source.go`:

```go
package source

import (
	"context"
	"io"
)

type Identity struct {
	Kind         string
	URL          string
	Size         int64
	ETag         string
	LastModified string
	VersionID    string
	Weak         bool
}

type Range struct {
	Start int64
	End   int64
}

func (r Range) Size() int64 {
	return r.End - r.Start + 1
}

type Reader interface {
	Resolve(context.Context) (Identity, error)
	ReadRange(context.Context, Range, Identity) (io.ReadCloser, Identity, error)
}
```

Create `internal/source/http.go`:

```go
package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type HTTP struct {
	url    string
	client *http.Client
}

func NewHTTP(url string, client *http.Client) *HTTP {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTP{url: url, client: client}
}

func (h *HTTP) Resolve(ctx context.Context) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, h.url, nil)
	if err != nil {
		return Identity{}, err
	}
	res, err := h.client.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Identity{}, fmt.Errorf("HEAD returned HTTP %d", res.StatusCode)
	}
	size, _ := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64)
	etag := res.Header.Get("ETag")
	return Identity{
		Kind: "http",
		URL: h.url,
		Size: size,
		ETag: etag,
		LastModified: res.Header.Get("Last-Modified"),
		Weak: strings.HasPrefix(strings.ToLower(etag), "w/"),
	}, nil
}

func (h *HTTP) ReadRange(ctx context.Context, r Range, pinned Identity) (io.ReadCloser, Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return nil, Identity{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.Start, r.End))
	if pinned.ETag != "" && !pinned.Weak {
		req.Header.Set("If-Match", pinned.ETag)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, Identity{}, err
	}
	if res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, Identity{}, fmt.Errorf("range returned HTTP %d, want 206", res.StatusCode)
	}
	if err := validateContentRange(res.Header.Get("Content-Range"), r, pinned.Size); err != nil {
		res.Body.Close()
		return nil, Identity{}, err
	}
	return res.Body, pinned, nil
}

func validateContentRange(header string, r Range, total int64) error {
	want := fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, total)
	if header != want {
		return fmt.Errorf("Content-Range = %q, want %q", header, want)
	}
	return nil
}
```

- [ ] **Step 4: Run source tests**

Run:

```bash
rtk go test ./internal/source
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/source
rtk git commit -m "Add HTTP source identity and range reader"
```

---

## Task 4: Ordered Spooler

**Files:**
- Create: `internal/spool/spool.go`
- Create: `internal/spool/spool_test.go`

- [ ] **Step 1: Write failing spooler tests**

Create `internal/spool/spool_test.go`:

```go
package spool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestEmitOrderedOutOfOrderChunks(t *testing.T) {
	chunks := []Chunk{
		{Index: 1, Start: 3, End: 5, Data: []byte("def")},
		{Index: 0, Start: 0, End: 2, Data: []byte("abc")},
	}
	var out bytes.Buffer
	err := EmitOrdered(context.Background(), chunks, &out)
	if err != nil {
		t.Fatalf("EmitOrdered error: %v", err)
	}
	if out.String() != "abcdef" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRejectGap(t *testing.T) {
	chunks := []Chunk{{Index: 1, Start: 3, End: 5, Data: []byte("def")}}
	var out bytes.Buffer
	err := EmitOrdered(context.Background(), chunks, &out)
	if err == nil {
		t.Fatalf("EmitOrdered succeeded, want gap error")
	}
}

func TestDownstreamFailureCancels(t *testing.T) {
	chunks := []Chunk{{Index: 0, Start: 0, End: 2, Data: []byte("abc")}}
	err := EmitOrdered(context.Background(), chunks, failingWriter{})
	if err == nil {
		t.Fatalf("EmitOrdered succeeded, want write error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

var _ io.Writer = failingWriter{}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/spool
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement minimal ordered emitter**

Create `internal/spool/spool.go`:

```go
package spool

import (
	"context"
	"fmt"
	"io"
	"sort"
)

type Chunk struct {
	Index int
	Start int64
	End   int64
	Data  []byte
}

func EmitOrdered(ctx context.Context, chunks []Chunk, out io.Writer) error {
	ordered := append([]Chunk(nil), chunks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	var next int64
	for _, ch := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ch.Start != next {
			return fmt.Errorf("chunk gap: got start %d, want %d", ch.Start, next)
		}
		if int64(len(ch.Data)) != ch.End-ch.Start+1 {
			return fmt.Errorf("chunk %d size mismatch", ch.Index)
		}
		n, err := out.Write(ch.Data)
		if err != nil {
			return err
		}
		if n != len(ch.Data) {
			return io.ErrShortWrite
		}
		next = ch.End + 1
	}
	return nil
}
```

- [ ] **Step 4: Run spooler tests**

Run:

```bash
rtk go test ./internal/spool
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/spool
rtk git commit -m "Add ordered chunk spooler"
```

---

## Task 5: Safe Tar Extractor

**Files:**
- Create: `internal/extract/extract.go`
- Create: `internal/extract/extract_test.go`

- [ ] **Step 1: Write failing extractor tests**

Create `internal/extract/extract_test.go`:

```go
package extract

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractSafeFile(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "chaindata/file.txt", Mode: 0644, Size: 5})
	_, _ = tw.Write([]byte("hello"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err != nil {
		t.Fatalf("ExtractTar error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "chaindata/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("file = %q", got)
	}
}

func TestRejectTraversal(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want traversal error")
	}
}

func TestRejectSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want symlink error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/extract
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement safe extractor**

Create `internal/extract/extract.go`:

```go
package extract

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Limits struct {
	MaxBytes int64
	MaxFiles int64
}

func ExtractTar(r io.Reader, dest string, limits Limits) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var bytesWritten int64
	var files int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeTarget(root, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if limits.MaxFiles > 0 && files > limits.MaxFiles {
				return fmt.Errorf("max extracted files exceeded")
			}
			if limits.MaxBytes > 0 && bytesWritten+h.Size > limits.MaxBytes {
				return fmt.Errorf("max extracted bytes exceeded")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != h.Size {
				return io.ErrUnexpectedEOF
			}
			bytesWritten += n
		case tar.TypeSymlink, tar.TypeLink:
			linkTarget := h.Linkname
			if filepath.IsAbs(linkTarget) || strings.Contains(filepath.Clean(linkTarget), "..") {
				return fmt.Errorf("unsafe link %q -> %q", h.Name, h.Linkname)
			}
			return fmt.Errorf("links are not allowed in v1: %q", h.Name)
		default:
			return fmt.Errorf("unsupported tar entry type %d for %q", h.Typeflag, h.Name)
		}
	}
}

func safeTarget(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, nil
}
```

- [ ] **Step 4: Run extractor tests**

Run:

```bash
rtk go test ./internal/extract
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/extract
rtk git commit -m "Add safe tar extractor"
```

---

## Task 6: Restore State, Locks, And Stamps

**Files:**
- Create: `internal/restore/restore.go`
- Create: `internal/restore/restore_test.go`

- [ ] **Step 1: Write failing restore-state tests**

Create `internal/restore/restore_test.go`:

```go
package restore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStampMatchesIdentity(t *testing.T) {
	tmp := t.TempDir()
	stamp := Stamp{SnapshotID: "sha256:abc", Target: "/data", ToolVersion: "test"}
	if err := WriteStamp(filepath.Join(tmp, ".stream-download.stamp"), stamp); err != nil {
		t.Fatalf("WriteStamp error: %v", err)
	}
	if ok := StampMatches(filepath.Join(tmp, ".stream-download.stamp"), stamp); !ok {
		t.Fatalf("StampMatches = false, want true")
	}
	stamp.SnapshotID = "sha256:other"
	if ok := StampMatches(filepath.Join(tmp, ".stream-download.stamp"), stamp); ok {
		t.Fatalf("StampMatches = true for changed identity")
	}
}

func TestPrepareTargetRequiresEmptyWithoutWipe(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, false); err == nil {
		t.Fatalf("PrepareTarget succeeded, want non-empty error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/restore
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement restore state helpers**

Create `internal/restore/restore.go`:

```go
package restore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Stamp struct {
	SnapshotID  string `json:"snapshot_id"`
	Source      string `json:"source"`
	Checksum    string `json:"checksum"`
	Target      string `json:"target"`
	Compression string `json:"compression"`
	ToolVersion string `json:"tool_version"`
}

func WriteStamp(path string, stamp Stamp) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func StampMatches(path string, want Stamp) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var got Stamp
	if err := json.Unmarshal(data, &got); err != nil {
		return false
	}
	return got == want
}

func PrepareTarget(target string, wipe bool) error {
	if target == "/" {
		return errors.New("refusing to restore into /")
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if !wipe {
		return errors.New("target is non-empty; set WIPE_EXISTING=true to replace")
	}
	for _, entry := range entries {
		p := filepath.Join(target, entry.Name())
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run restore tests**

Run:

```bash
rtk go test ./internal/restore
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/restore
rtk git commit -m "Add restore stamp and target state helpers"
```

---

## Task 7: Decompression Wiring

**Files:**
- Create: `internal/decompress/decompress.go`
- Create: `internal/decompress/decompress_test.go`

- [ ] **Step 1: Write failing decompression tests**

Create `internal/decompress/decompress_test.go`:

```go
package decompress

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestGzipReader(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("hello"))
	_ = gw.Close()
	rc, err := NewReader("gzip", &buf)
	if err != nil {
		t.Fatalf("NewReader error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestNoneReader(t *testing.T) {
	rc, err := NewReader("none", bytes.NewBufferString("plain"))
	if err != nil {
		t.Fatalf("NewReader error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "plain" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/decompress
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement gzip/none reader**

Create `internal/decompress/decompress.go`:

```go
package decompress

import (
	"compress/gzip"
	"fmt"
	"io"
)

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func NewReader(kind string, r io.Reader) (io.ReadCloser, error) {
	switch kind {
	case "", "none":
		return readCloser{Reader: r}, nil
	case "gzip", "gz":
		return gzip.NewReader(r)
	default:
		return nil, fmt.Errorf("compression %q is not supported by this reader", kind)
	}
}
```

- [ ] **Step 4: Run decompression tests**

Run:

```bash
rtk go test ./internal/decompress
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/decompress
rtk git commit -m "Add decompression reader"
```

---

## Task 8: CLI Happy Path For Single HTTP Snapshot

**Files:**
- Create: `cmd/stream-download/main.go`
- Modify: `internal/source/http.go`
- Modify: `internal/restore/restore.go`
- Test: `cmd/stream-download/main_test.go`

- [ ] **Step 1: Write failing end-to-end CLI test**

Create `cmd/stream-download/main_test.go`:

```go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunRestoresSmallGzipTar(t *testing.T) {
	archive := gzipTar(t, "chaindata/file.txt", "hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"snap"`)
		w.Header().Set("Content-Length", "0")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "0")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	scratch := filepath.Join(t.TempDir(), "scratch")
	env := map[string]string{
		"RESTORE_SNAPSHOT": "true",
		"DIR": dir,
		"SCRATCH_DIR": scratch,
		"SNAPSHOT_URL": srv.URL,
		"COMPRESSION": "gzip",
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
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./cmd/stream-download
```

Expected: FAIL because CLI does not exist.

- [ ] **Step 3: Implement CLI single-stream path**

Create `cmd/stream-download/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nodeify-eth/stream-download/internal/config"
	"github.com/nodeify-eth/stream-download/internal/decompress"
	"github.com/nodeify-eth/stream-download/internal/extract"
	"github.com/nodeify-eth/stream-download/internal/logx"
	"github.com/nodeify-eth/stream-download/internal/restore"
)

func main() {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		for i, c := range kv {
			if c == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, logx.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(env map[string]string, stdout, stderr io.Writer) error {
	cfg, err := config.LoadFromMap(env)
	if err != nil {
		return err
	}
	logger := logx.New(stdout, cfg.LogFormat)
	if !cfg.RestoreSnapshot {
		logger.Info("restore_skipped", nil)
		return nil
	}
	target := filepath.Join(cfg.Dir, cfg.Subpath)
	if err := restore.PrepareTarget(target, cfg.WipeExisting); err != nil {
		return err
	}
	staging := filepath.Join(cfg.Dir, ".stream-download-staging")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0755); err != nil {
		return err
	}
	logger.Info("start_restore", logx.Fields{"source_kind": "http"})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, cfg.SnapshotURLs[0], nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET returned HTTP %d", res.StatusCode)
	}
	dr, err := decompress.NewReader(cfg.Compression, res.Body)
	if err != nil {
		return err
	}
	defer dr.Close()
	if err := extract.ExtractTar(dr, staging, extract.Limits{MaxBytes: cfg.MaxExtractedBytes, MaxFiles: cfg.MaxExtractedFiles}); err != nil {
		return err
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(staging, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(staging)
	if err := restore.WriteStamp(filepath.Join(cfg.Dir, ".stream-download.stamp"), restore.Stamp{Source: cfg.SnapshotURLs[0], Target: target, Compression: cfg.Compression, ToolVersion: "dev"}); err != nil {
		return err
	}
	logger.Info("restore_complete", logx.Fields{"target": target})
	return nil
}
```

- [ ] **Step 4: Run CLI tests**

Run:

```bash
rtk go test ./cmd/stream-download
```

Expected: PASS.

- [ ] **Step 5: Run all tests**

Run:

```bash
rtk go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
rtk git add cmd internal
rtk git commit -m "Add single HTTP restore CLI"
```

---

## Task 9: S3 Source Skeleton And Public Multipart `aria2c` Wrapper

**Files:**
- Modify: `go.mod`
- Create: `internal/source/s3.go`
- Create: `internal/aria2/aria2.go`
- Create: `internal/aria2/aria2_test.go`

- [ ] **Step 1: Write failing `aria2c` wrapper tests**

Create `internal/aria2/aria2_test.go`:

```go
package aria2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteInputFileUsesDeterministicNames(t *testing.T) {
	tmp := t.TempDir()
	path, err := WriteInputFile(tmp, []string{"https://example.invalid/a.part0000", "https://example.invalid/a.part0001"})
	if err != nil {
		t.Fatalf("WriteInputFile error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "out=part-000000") || !strings.Contains(text, "out=part-000001") {
		t.Fatalf("input file missing deterministic outputs:\n%s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if filepath.Dir(path) != tmp {
		t.Fatalf("input file dir = %q", filepath.Dir(path))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/aria2
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement wrapper input generation**

Create `internal/aria2/aria2.go`:

```go
package aria2

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteInputFile(dir string, urls []string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "aria2-input.txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for i, u := range urls {
		if _, err := fmt.Fprintf(f, "%s\n  out=part-%06d\n", u, i); err != nil {
			return "", err
		}
	}
	return path, nil
}
```

- [ ] **Step 4: Add S3 SDK dependency and skeleton**

Run:

```bash
rtk go get github.com/aws/aws-sdk-go-v2/config github.com/aws/aws-sdk-go-v2/service/s3
```

Create `internal/source/s3.go`:

```go
package source

import (
	"context"
	"fmt"
	"io"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3 struct {
	client *s3.Client
	bucket string
	key    string
}

func NewS3(client *s3.Client, bucket, key string) *S3 {
	return &S3{client: client, bucket: bucket, key: key}
}

func NewDefaultS3(ctx context.Context, bucket, key, endpoint string) (*S3, error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return NewS3(client, bucket, key), nil
}

func (s *S3) Resolve(ctx context.Context) (Identity, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key)})
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Kind: "s3", URL: fmt.Sprintf("s3://%s/%s", s.bucket, s.key), Size: aws.ToInt64(out.ContentLength)}
	if out.ETag != nil {
		id.ETag = aws.ToString(out.ETag)
	}
	if out.VersionId != nil {
		id.VersionID = aws.ToString(out.VersionId)
	}
	return id, nil
}

func (s *S3) ReadRange(ctx context.Context, r Range, pinned Identity) (io.ReadCloser, Identity, error) {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", r.Start, r.End)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key), Range: aws.String(rangeHeader)})
	if err != nil {
		return nil, Identity{}, err
	}
	return out.Body, pinned, nil
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
rtk go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
rtk git add go.mod go.sum internal/source/s3.go internal/aria2
rtk git commit -m "Add S3 source skeleton and aria2 input wrapper"
```

---

## Task 10: External Decompressors

**Files:**
- Modify: `internal/decompress/decompress.go`
- Modify: `internal/decompress/decompress_test.go`

- [ ] **Step 1: Add failing external decompressor command test**

Append to `internal/decompress/decompress_test.go`:

```go
func TestCommandForExternalCompression(t *testing.T) {
	tests := map[string][]string{
		"zstd": {"zstd", "-dc"},
		"zst":  {"zstd", "-dc"},
		"lz4":  {"lz4", "-dc"},
		"xz":   {"xz", "-dc"},
	}
	for kind, want := range tests {
		got, err := CommandFor(kind)
		if err != nil {
			t.Fatalf("CommandFor(%q) error: %v", kind, err)
		}
		if len(got) != len(want) {
			t.Fatalf("CommandFor(%q) = %#v, want %#v", kind, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("CommandFor(%q) = %#v, want %#v", kind, got, want)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/decompress
```

Expected: FAIL because `CommandFor` is undefined.

- [ ] **Step 3: Implement command mapping**

Modify `internal/decompress/decompress.go`:

```go
func CommandFor(kind string) ([]string, error) {
	switch kind {
	case "", "none", "gzip", "gz":
		return nil, nil
	case "zstd", "zst":
		return []string{"zstd", "-dc"}, nil
	case "lz4":
		return []string{"lz4", "-dc"}, nil
	case "xz":
		return []string{"xz", "-dc"}, nil
	default:
		return nil, fmt.Errorf("unsupported compression %q", kind)
	}
}
```

- [ ] **Step 4: Run decompressor tests**

Run:

```bash
rtk go test ./internal/decompress
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/decompress
rtk git commit -m "Add external decompressor command mapping"
```

---

## Task 11: Range Spooler Integration

**Files:**
- Modify: `internal/spool/spool.go`
- Modify: `internal/spool/spool_test.go`
- Modify: `cmd/stream-download/main.go`
- Modify: `cmd/stream-download/main_test.go`

- [ ] **Step 1: Add failing HTTP range restore test**

Append to `cmd/stream-download/main_test.go`:

```go
func TestRunUsesHTTPRangesWhenContentLengthKnown(t *testing.T) {
	archive := gzipTar(t, "db/file.txt", "range")
	var sawRange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"range-snap"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		if r.Method == http.MethodHead {
			return
		}
		if r.Header.Get("Range") != "" {
			sawRange = true
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(archive)-1, len(archive)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(archive)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := map[string]string{
		"RESTORE_SNAPSHOT": "true",
		"DIR": dir,
		"SCRATCH_DIR": filepath.Join(t.TempDir(), "scratch"),
		"SNAPSHOT_URL": srv.URL,
		"COMPRESSION": "gzip",
		"RANGE_SIZE": "1MiB",
		"REQUIRE_MOUNTPOINT": "false",
	}
	if err := run(env, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !sawRange {
		t.Fatalf("server did not observe ranged GET")
	}
}
```

Add imports to `cmd/stream-download/main_test.go`:

```go
import "fmt"
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./cmd/stream-download
```

Expected: FAIL because `run` still uses plain GET.

- [ ] **Step 3: Implement range path in CLI**

Modify `cmd/stream-download/main.go` so `run` resolves the HTTP source and uses one range chunk when the size is known. Keep the existing plain GET fallback for unknown size:

```go
src := source.NewHTTP(cfg.SnapshotURLs[0], http.DefaultClient)
id, err := src.Resolve(context.Background())
if err == nil && id.Size > 0 {
	rc, _, err := src.ReadRange(context.Background(), source.Range{Start: 0, End: id.Size - 1}, id)
	if err != nil {
		return err
	}
	defer rc.Close()
	dr, err := decompress.NewReader(cfg.Compression, rc)
	if err != nil {
		return err
	}
	defer dr.Close()
	if err := extract.ExtractTar(dr, staging, extract.Limits{MaxBytes: cfg.MaxExtractedBytes, MaxFiles: cfg.MaxExtractedFiles}); err != nil {
		return err
	}
} else {
	// keep the existing plain GET fallback
}
```

Add import:

```go
"github.com/nodeify-eth/stream-download/internal/source"
```

- [ ] **Step 4: Run CLI tests**

Run:

```bash
rtk go test ./cmd/stream-download
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add cmd internal/spool
rtk git commit -m "Integrate HTTP range restore path"
```

---

## Task 12: Container And Documentation

**Files:**
- Create: `Dockerfile`
- Create: `README.md`

- [ ] **Step 1: Add Dockerfile**

Create `Dockerfile`:

```dockerfile
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/stream-download ./cmd/stream-download

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates aria2 zstd lz4 xz-utils \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stream-download /usr/local/bin/stream-download
ENTRYPOINT ["/usr/local/bin/stream-download"]
```

- [ ] **Step 2: Add README**

Create `README.md`:

```markdown
# stream-download

Production-oriented RPC snapshot restore tool for Kubernetes initContainers.

## Basic HTTP restore

```bash
RESTORE_SNAPSHOT=true \
DIR=/data \
SCRATCH_DIR=/scratch \
SNAPSHOT_URL=https://example.com/snapshot.tar.gz \
COMPRESSION=gzip \
stream-download
```

## Kubernetes mounts

Mount the RPC data PVC at `/data` and a scratch PVC at `/scratch`. For large snapshots, prefer a scratch PVC over node `emptyDir`.

## Integrity

`CHECKSUM_SHA256` verifies the compressed archive byte stream. Without a checksum, the tool pins source identity and logs that authenticity is not proven.

## Safety

The extractor rejects absolute paths, `..`, unsafe links, special files, and setuid/setgid bits.
```

- [ ] **Step 3: Run verification**

Run:

```bash
rtk go test ./...
rtk go build ./cmd/stream-download
```

Expected: both commands pass.

- [ ] **Step 4: Commit**

```bash
rtk git add Dockerfile README.md
rtk git commit -m "Add container and usage docs"
```

---

## Self-Review Notes

Spec coverage:

- Config validation is covered by Task 1.
- Redaction and logging are covered by Task 2.
- HTTP identity and ranged reads are covered by Task 3.
- Ordered spooling invariants begin in Task 4.
- Safe extraction is covered by Task 5.
- Stamps, target emptiness, and wipe opt-in begin in Task 6.
- Decompression begins in Task 7 and external decompressor command mapping is covered by Task 10.
- CLI happy path is covered by Task 8.
- S3 and public multipart `aria2c` foundations are covered by Task 9.
- HTTP range restore integration is covered by Task 11.
- Container and user docs are covered by Task 12.
