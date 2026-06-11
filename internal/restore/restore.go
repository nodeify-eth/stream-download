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
