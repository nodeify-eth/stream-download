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
	stampPath := filepath.Join(cfg.Dir, ".stream-download.stamp")
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
	if err := restore.PrepareTarget(target, cfg.WipeExisting); err != nil {
		return err
	}
	staging := filepath.Join(cfg.Dir, ".stream-download-staging")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0755); err != nil {
		return err
	}
	logger.Info("start_restore", logx.Fields{"source_kind": "http"})

	stream, cleanup, err := openSnapshotStream(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	var checksum hash.Hash
	if cfg.ChecksumSHA256 != "" {
		checksum = sha256.New()
		stream = io.TeeReader(stream, checksum)
	}
	dr, err := decompress.NewReader(compression, stream)
	if err != nil {
		return err
	}
	if err := extract.ExtractTar(dr, staging, extract.Limits{MaxBytes: cfg.MaxExtractedBytes, MaxFiles: cfg.MaxExtractedFiles}); err != nil {
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

func openSnapshotStream(ctx context.Context, cfg config.Config) (io.Reader, func(), error) {
	src, err := sourceFromConfig(ctx, cfg)
	if err != nil {
		return nil, func() {}, err
	}
	id, err := src.Resolve(ctx)
	if err == nil && id.Size > 0 && canUseRangeIdentity(id, cfg.AllowWeakIdentity) {
		rc, err := spool.StreamRanges(ctx, src, id, cfg.RangeSize, cfg.DownloadConcurrency, cfg.ScratchDir)
		if err != nil {
			return nil, func() {}, err
		}
		return rc, func() { _ = rc.Close() }, nil
	}
	if cfg.S3Bucket != "" {
		if err != nil {
			return nil, func() {}, err
		}
		return nil, func() {}, fmt.Errorf("S3 source size must be known")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.SnapshotURLs[0], nil)
	if err != nil {
		return nil, func() {}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, func() {}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, func() {}, fmt.Errorf("GET returned HTTP %d", res.StatusCode)
	}
	return res.Body, func() { _ = res.Body.Close() }, nil
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
