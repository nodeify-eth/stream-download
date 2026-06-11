package main

import (
	"context"
	"fmt"
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
	if len(cfg.SnapshotURLs) == 0 {
		return fmt.Errorf("no snapshot URL configured")
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

	stream, cleanup, err := openSnapshotStream(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	compression := compressionFor(cfg.Compression, cfg.SnapshotURLs[0])
	dr, err := decompress.NewReader(compression, stream)
	if err != nil {
		return err
	}
	defer dr.Close()
	if err := extract.ExtractTar(dr, staging, extract.Limits{MaxBytes: cfg.MaxExtractedBytes, MaxFiles: cfg.MaxExtractedFiles}); err != nil {
		return err
	}
	if err := moveChildren(staging, target); err != nil {
		return err
	}
	_ = os.RemoveAll(staging)
	stamp := restore.Stamp{Source: cfg.SnapshotURLs[0], Target: target, Compression: compression, ToolVersion: "dev"}
	if err := restore.WriteStamp(filepath.Join(cfg.Dir, ".stream-download.stamp"), stamp); err != nil {
		return err
	}
	logger.Info("restore_complete", logx.Fields{"target": target})
	return nil
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

func openSnapshotStream(ctx context.Context, cfg config.Config) (io.Reader, func(), error) {
	src := source.NewHTTP(cfg.SnapshotURLs[0], http.DefaultClient)
	id, err := src.Resolve(ctx)
	if err == nil && id.Size > 0 {
		rc, err := spool.StreamRanges(ctx, src, id, cfg.RangeSize, cfg.DownloadConcurrency, cfg.ScratchDir)
		if err != nil {
			return nil, func() {}, err
		}
		return rc, func() { _ = rc.Close() }, nil
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
