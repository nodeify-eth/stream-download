package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nodeify-eth/stream-download/internal/config"
	"github.com/nodeify-eth/stream-download/internal/decompress"
	"github.com/nodeify-eth/stream-download/internal/extract"
	"github.com/nodeify-eth/stream-download/internal/logx"
	"github.com/nodeify-eth/stream-download/internal/restore"
	"github.com/nodeify-eth/stream-download/internal/source"
	"github.com/nodeify-eth/stream-download/internal/spool"
)

func main() {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			env[key] = value
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
	sourceName := snapshotSourceName(cfg)
	compression := compressionFor(cfg.Compression, sourceName)
	stampPath := filepath.Join(cfg.Dir, restore.StampFileName)
	stamp := restore.Stamp{
		Source:      sourceName,
		Checksum:    cfg.ChecksumSHA256,
		Target:      target,
		Compression: compression,
		ToolVersion: "dev",
	}
	if restore.StampMatches(stampPath, stamp) {
		logger.Info("restore_already_complete", logx.Fields{"target": target})
		return nil
	}
	staging := filepath.Join(cfg.Dir, restore.StagingDirName)
	_ = os.RemoveAll(staging)
	if err := restore.PrepareTarget(target, cfg.WipeExisting); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0755); err != nil {
		return err
	}
	stream, cleanup, meta, err := openSnapshotStream(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	startFields := logx.Fields{
		"source_kind":          meta.sourceKind,
		"source_size_bytes":    meta.sourceSize,
		"range_mode":           meta.rangeMode,
		"range_size_bytes":     cfg.RangeSize,
		"download_concurrency": cfg.DownloadConcurrency,
		"compression":          compression,
		"target":               target,
	}
	if meta.sourceSize > 0 {
		if meta.rangeMode {
			startFields["message"] = fmt.Sprintf(
				"restoring %s %s snapshot to %s using %d concurrent %s ranges",
				formatBytes(float64(meta.sourceSize)),
				compression,
				target,
				cfg.DownloadConcurrency,
				formatBytes(float64(cfg.RangeSize)),
			)
		} else {
			startFields["message"] = fmt.Sprintf("restoring %s %s snapshot to %s using single stream", formatBytes(float64(meta.sourceSize)), compression, target)
		}
	} else {
		startFields["message"] = fmt.Sprintf("restoring %s snapshot to %s", compression, target)
	}
	logger.Info("start_restore", startFields)
	stream = newProgressReader(stream, logger, meta)

	var checksum hash.Hash
	if cfg.ChecksumSHA256 != "" {
		checksum = sha256.New()
		stream = io.TeeReader(stream, checksum)
	}
	dr, err := decompress.NewReader(compression, stream)
	if err != nil {
		return err
	}
	if err := extract.ExtractTar(dr, staging, extract.Limits{
		MaxBytes:        cfg.MaxExtractedBytes,
		MaxFiles:        cfg.MaxExtractedFiles,
		StripComponents: cfg.StripComponents,
	}); err != nil {
		_ = dr.Close()
		return err
	}
	if err := dr.Close(); err != nil {
		return err
	}
	if checksum != nil {
		got := hex.EncodeToString(checksum.Sum(nil))
		if got != cfg.ChecksumSHA256 {
			_ = os.RemoveAll(staging)
			return fmt.Errorf("checksum mismatch: sha256 %s, want %s", got, cfg.ChecksumSHA256)
		}
	}
	if err := moveChildren(staging, target); err != nil {
		return err
	}
	_ = os.RemoveAll(staging)
	if err := restore.WriteStamp(stampPath, stamp); err != nil {
		return err
	}
	logger.Info("restore_complete", logx.Fields{"target": target})
	return nil
}

func snapshotSourceName(cfg config.Config) string {
	if len(cfg.SnapshotURLs) > 0 {
		return cfg.SnapshotURLs[0]
	}
	return fmt.Sprintf("s3://%s/%s", cfg.S3Bucket, cfg.S3Key)
}

func compressionFor(configured, snapshotURL string) string {
	if configured != "" && configured != "auto" {
		return configured
	}
	clean := strings.ToLower(strings.Split(snapshotURL, "?")[0])
	switch {
	case strings.HasSuffix(clean, ".tar.gz"), strings.HasSuffix(clean, ".tgz"):
		return "gzip"
	case strings.HasSuffix(clean, ".tar.zst"), strings.HasSuffix(clean, ".tar.zstd"):
		return "zstd"
	case strings.HasSuffix(clean, ".tar.lz4"):
		return "lz4"
	case strings.HasSuffix(clean, ".tar.xz"), strings.HasSuffix(clean, ".txz"):
		return "xz"
	case strings.HasSuffix(clean, ".tar"):
		return "none"
	default:
		return configured
	}
}

var newS3Source = func(ctx context.Context, bucket, key, endpoint string) (source.Reader, error) {
	return source.NewDefaultS3(ctx, bucket, key, endpoint)
}

type streamMetadata struct {
	sourceKind string
	sourceSize int64
	rangeMode  bool
}

func openSnapshotStream(ctx context.Context, cfg config.Config) (io.Reader, func(), streamMetadata, error) {
	src, err := sourceFromConfig(ctx, cfg)
	if err != nil {
		return nil, func() {}, streamMetadata{}, err
	}
	id, err := src.Resolve(ctx)
	if err == nil && id.Size > 0 && canUseRangeIdentity(id, cfg.AllowWeakIdentity) {
		rc, err := spool.StreamRanges(ctx, src, id, cfg.RangeSize, cfg.DownloadConcurrency, cfg.ScratchDir, cfg.MaxRetries)
		if err != nil {
			return nil, func() {}, streamMetadata{}, err
		}
		return rc, func() { _ = rc.Close() }, streamMetadata{sourceKind: id.Kind, sourceSize: id.Size, rangeMode: true}, nil
	}
	if cfg.S3Bucket != "" {
		if err != nil {
			return nil, func() {}, streamMetadata{}, err
		}
		return nil, func() {}, streamMetadata{}, fmt.Errorf("S3 source size must be known")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.SnapshotURLs[0], nil)
	if err != nil {
		return nil, func() {}, streamMetadata{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, func() {}, streamMetadata{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, func() {}, streamMetadata{}, fmt.Errorf("GET returned HTTP %d", res.StatusCode)
	}
	size := int64(0)
	if err == nil && id.Size > 0 {
		size = id.Size
	}
	return res.Body, func() { _ = res.Body.Close() }, streamMetadata{sourceKind: "http", sourceSize: size}, nil
}

func canUseRangeIdentity(id source.Identity, allowWeak bool) bool {
	if id.Kind == "s3" {
		return id.VersionID != "" || id.ETag != "" || allowWeak
	}
	if id.ETag == "" {
		return allowWeak
	}
	return !id.Weak || allowWeak
}

func sourceFromConfig(ctx context.Context, cfg config.Config) (source.Reader, error) {
	if cfg.S3Bucket != "" {
		return newS3Source(ctx, cfg.S3Bucket, cfg.S3Key, cfg.S3EndpointURL)
	}
	if len(cfg.SnapshotURLs) == 0 {
		return nil, fmt.Errorf("no snapshot URL configured")
	}
	return source.NewHTTP(cfg.SnapshotURLs[0], http.DefaultClient), nil
}

func moveChildren(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type progressReader struct {
	r        io.Reader
	logger   *logx.Logger
	meta     streamMetadata
	started  time.Time
	lastLog  time.Time
	read     int64
	complete bool
}

func newProgressReader(r io.Reader, logger *logx.Logger, meta streamMetadata) io.Reader {
	return &progressReader{r: r, logger: logger, meta: meta}
}

func (p *progressReader) Read(b []byte) (int, error) {
	if p.started.IsZero() {
		p.started = time.Now()
	}
	n, err := p.r.Read(b)
	now := time.Now()
	if n > 0 {
		p.read += int64(n)
		p.log(now, false)
	}
	if err == io.EOF && !p.complete {
		p.complete = true
		p.log(time.Now(), true)
	}
	return n, err
}

func (p *progressReader) log(now time.Time, complete bool) {
	if !complete && !p.lastLog.IsZero() && now.Sub(p.lastLog) < 30*time.Second {
		return
	}
	p.lastLog = now
	elapsed := now.Sub(p.started).Seconds()
	fields := logx.Fields{
		"compressed_bytes_read": p.read,
		"source_size_bytes":     p.meta.sourceSize,
		"source_kind":           p.meta.sourceKind,
		"range_mode":            p.meta.rangeMode,
		"elapsed_seconds":       int64(elapsed),
	}
	if elapsed >= 5 && p.read >= 1024*1024 {
		bytesPerSecond := float64(p.read) / elapsed
		fields["bytes_per_second"] = bytesPerSecond
		if p.meta.sourceSize > 0 {
			remaining := p.meta.sourceSize - p.read
			if remaining < 0 {
				remaining = 0
			}
			if bytesPerSecond > 0 {
				fields["eta_seconds"] = int64(float64(remaining) / bytesPerSecond)
			}
		}
	}
	if p.meta.sourceSize > 0 {
		fields["percent_complete"] = float64(p.read) * 100 / float64(p.meta.sourceSize)
	}
	fields["message"] = p.message(fields, complete)
	if complete {
		p.logger.Info("restore_stream_complete", fields)
		return
	}
	p.logger.Info("restore_progress", fields)
}

func (p *progressReader) message(fields logx.Fields, complete bool) string {
	percent, hasPercent := fields["percent_complete"].(float64)
	elapsed, _ := fields["elapsed_seconds"].(int64)
	rate, hasRate := fields["bytes_per_second"].(float64)
	eta, hasETA := fields["eta_seconds"].(int64)

	prefix := "downloaded"
	if complete {
		prefix = "download complete"
	}
	parts := []string{}
	if hasPercent {
		parts = append(parts, fmt.Sprintf("%s %s", formatPercent(percent), prefix))
	} else {
		parts = append(parts, prefix)
	}
	if hasRate {
		parts = append(parts, fmt.Sprintf("%s/s", formatBytes(rate)))
	}
	if hasETA && !complete {
		parts = append(parts, fmt.Sprintf("ETA %s", formatDuration(eta)))
	}
	parts = append(parts, fmt.Sprintf("elapsed %s", formatDuration(elapsed)))
	if p.meta.sourceSize > 0 {
		parts = append(parts, fmt.Sprintf("(%s / %s)", formatBytes(float64(p.read)), formatBytes(float64(p.meta.sourceSize))))
	} else {
		parts = append(parts, fmt.Sprintf("(%s)", formatBytes(float64(p.read))))
	}
	return strings.Join(parts, ", ")
}

func formatPercent(v float64) string {
	switch {
	case v >= 10:
		return fmt.Sprintf("%.1f%%", v)
	case v >= 1:
		return fmt.Sprintf("%.2f%%", v)
	default:
		return fmt.Sprintf("%.3f%%", v)
	}
}

func formatBytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

func formatDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
