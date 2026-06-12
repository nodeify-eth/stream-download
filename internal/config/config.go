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
	RestoreSnapshot     bool
	Dir                 string
	Subpath             string
	ScratchDir          string
	SnapshotURL         string
	SnapshotURLs        []string
	S3EndpointURL       string
	S3Bucket            string
	S3Key               string
	ChecksumSHA256      string
	RequireChecksum     bool
	AllowWeakIdentity   bool
	DownloadConcurrency int
	DownloadWindowBytes int64
	RangeSize           int64
	MaxExtractedBytes   int64
	MaxExtractedFiles   int64
	StripComponents     int
	Compression         string
	LogFormat           string
	MaxRetries          int
	StallTimeout        time.Duration
	WipeExisting        bool
	RequireMountpoint   bool
}

func LoadFromMap(env map[string]string) (Config, error) {
	restoreSnapshot, err := boolEnv(env, "RESTORE_SNAPSHOT", true)
	if err != nil {
		return Config{}, err
	}
	downloadConcurrency, err := intEnv(env, "DOWNLOAD_CONCURRENCY", 8)
	if err != nil {
		return Config{}, err
	}
	maxRetries, err := intEnv(env, "MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}
	stallTimeout, err := durationEnv(env, "STALL_TIMEOUT", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	requireMountpoint, err := boolEnv(env, "REQUIRE_MOUNTPOINT", true)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		RestoreSnapshot:     restoreSnapshot,
		DownloadConcurrency: downloadConcurrency,
		RangeSize:           defaultBytes(env, "RANGE_SIZE", 256*1024*1024),
		Compression:         stringEnv(env, "COMPRESSION", "auto"),
		LogFormat:           stringEnv(env, "LOG_FORMAT", "text"),
		MaxRetries:          maxRetries,
		StallTimeout:        stallTimeout,
		RequireMountpoint:   requireMountpoint,
	}
	cfg.DownloadWindowBytes = defaultBytes(env, "DOWNLOAD_WINDOW_BYTES", int64(cfg.DownloadConcurrency)*cfg.RangeSize)
	if !cfg.RestoreSnapshot {
		return cfg, nil
	}

	cfg.Dir = strings.TrimSpace(env["DIR"])
	cfg.Subpath = strings.TrimSpace(env["SUBPATH"])
	cfg.ScratchDir = strings.TrimSpace(env["SCRATCH_DIR"])
	cfg.SnapshotURL = strings.TrimSpace(env["SNAPSHOT_URL"])
	cfg.S3EndpointURL = strings.TrimSpace(env["S3_ENDPOINT_URL"])
	cfg.S3Bucket = strings.TrimSpace(env["S3_BUCKET"])
	cfg.S3Key = strings.TrimSpace(env["S3_KEY"])
	cfg.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(env["CHECKSUM_SHA256"]))
	if sourceType := strings.TrimSpace(env["SOURCE_TYPE"]); sourceType != "" && sourceType != "auto" {
		return cfg, fmt.Errorf("SOURCE_TYPE is not supported; leave it unset or set to auto")
	}
	if strings.TrimSpace(env["PROGRESS_STATE_FILE"]) != "" {
		return cfg, fmt.Errorf("PROGRESS_STATE_FILE is not supported")
	}
	cfg.RequireChecksum, err = boolEnv(env, "REQUIRE_CHECKSUM", false)
	if err != nil {
		return cfg, err
	}
	cfg.AllowWeakIdentity, err = boolEnv(env, "ALLOW_WEAK_IDENTITY", false)
	if err != nil {
		return cfg, err
	}
	cfg.WipeExisting, err = boolEnv(env, "WIPE_EXISTING", false)
	if err != nil {
		return cfg, err
	}
	cfg.MaxExtractedBytes = defaultBytes(env, "MAX_EXTRACTED_BYTES", 0)
	cfg.MaxExtractedFiles = defaultBytes(env, "MAX_EXTRACTED_FILES", 0)
	cfg.StripComponents, err = intEnv(env, "STRIP_COMPONENTS", 0)
	if err != nil {
		return cfg, err
	}

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
		if len(cfg.SnapshotURLs) > 1 {
			return cfg, errors.New("SNAPSHOT_URLS multipart restore is not supported yet; set a single SNAPSHOT_URL")
		}
	} else if cfg.SnapshotURL != "" {
		cfg.SnapshotURLs = []string{cfg.SnapshotURL}
	}

	hasHTTP := len(cfg.SnapshotURLs) > 0
	hasS3 := cfg.S3Bucket != "" || cfg.S3Key != ""
	if hasS3 && (cfg.S3Bucket == "" || cfg.S3Key == "") {
		return cfg, errors.New("S3_BUCKET and S3_KEY must be set together")
	}
	if hasHTTP && hasS3 {
		return cfg, errors.New("HTTP URL configuration and S3 bucket/key configuration are mutually exclusive")
	}
	if !hasHTTP && !hasS3 {
		return cfg, errors.New("set SNAPSHOT_URL, SNAPSHOT_URLS, or S3_BUCKET/S3_KEY")
	}
	if cfg.RequireChecksum && cfg.ChecksumSHA256 == "" {
		return cfg, errors.New("CHECKSUM_SHA256 is required when REQUIRE_CHECKSUM=true")
	}
	if cfg.ChecksumSHA256 != "" && !isSHA256Hex(cfg.ChecksumSHA256) {
		return cfg, errors.New("CHECKSUM_SHA256 must be a 64-character hex SHA-256 digest")
	}
	if cfg.DownloadConcurrency <= 0 || cfg.MaxRetries <= 0 {
		return cfg, errors.New("DOWNLOAD_CONCURRENCY and MAX_RETRIES must be positive")
	}
	if cfg.StallTimeout <= 0 {
		return cfg, errors.New("STALL_TIMEOUT must be positive")
	}
	if cfg.DownloadWindowBytes <= 0 || cfg.RangeSize <= 0 {
		return cfg, errors.New("DOWNLOAD_WINDOW_BYTES and RANGE_SIZE must be positive")
	}
	if cfg.MaxExtractedBytes < 0 || cfg.MaxExtractedFiles < 0 {
		return cfg, errors.New("MAX_EXTRACTED_BYTES and MAX_EXTRACTED_FILES must not be negative")
	}
	if cfg.StripComponents < 0 {
		return cfg, errors.New("STRIP_COMPONENTS must not be negative")
	}
	return cfg, nil
}

func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1024},
		{"MiB", 1024 * 1024},
		{"TiB", 1024 * 1024 * 1024 * 1024},
		{"GiB", 1024 * 1024 * 1024},
	}
	for _, unit := range units {
		if strings.HasSuffix(s, unit.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, unit.suffix), 10, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid byte value %q", s)
			}
			return n * unit.mult, nil
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

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func stringEnv(env map[string]string, key, fallback string) string {
	if v := strings.TrimSpace(env[key]); v != "" {
		return v
	}
	return fallback
}

func boolEnv(env map[string]string, key string, fallback bool) (bool, error) {
	if strings.TrimSpace(env[key]) == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(env[key])) {
	case "1", "true", "yes", "y":
		return true, nil
	case "0", "false", "no", "n":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be boolean", key)
	}
}

func intEnv(env map[string]string, key string, fallback int) (int, error) {
	if strings.TrimSpace(env[key]) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(env[key]))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return n, nil
}

func durationEnv(env map[string]string, key string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(env[key]) == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(env[key]))
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", key)
	}
	return d, nil
}

func defaultBytes(env map[string]string, key string, fallback int64) int64 {
	if strings.TrimSpace(env[key]) == "" {
		return fallback
	}
	n, err := ParseBytes(env[key])
	if err != nil {
		return -1
	}
	return n
}
