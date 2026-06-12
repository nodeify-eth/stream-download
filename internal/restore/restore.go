package restore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/nodeify-eth/stream-download/internal/logx"
)

const StampFileName = ".stream-download.stamp"
const StagingDirName = ".stream-download-staging"
const PublishFileName = ".stream-download-publishing.json"

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
	stamp.Source = logx.Redact(stamp.Source)
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func WritePublishMarker(target string, names []string) error {
	data, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(target, PublishFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ClearPublishMarker(target string) error {
	err := os.Remove(filepath.Join(target, PublishFileName))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func CleanupInterruptedPublish(target string) error {
	path := filepath.Join(target, PublishFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return err
	}
	for _, name := range names {
		if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || name == ".." {
			return errors.New("invalid interrupted publish marker")
		}
		if err := os.RemoveAll(filepath.Join(target, name)); err != nil {
			return err
		}
	}
	return ClearPublishMarker(target)
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
	if hasNoRestoredContent(entries) {
		return nil
	}
	if !wipe {
		return errors.New("target is non-empty; set WIPE_EXISTING=true to replace")
	}
	for _, entry := range entries {
		if preserveOnWipe(entry.Name()) {
			continue
		}
		p := filepath.Join(target, entry.Name())
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func RequireMountpoint(path string) error {
	ok, err := IsMountpoint(path)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("target is not a mountpoint; set REQUIRE_MOUNTPOINT=false to allow")
	}
	return nil
}

func hasNoRestoredContent(entries []os.DirEntry) bool {
	for _, entry := range entries {
		switch entry.Name() {
		case "lost+found", StampFileName, StagingDirName, PublishFileName:
			continue
		default:
			return false
		}
	}
	return true
}

func preserveOnWipe(name string) bool {
	return name == "lost+found"
}
