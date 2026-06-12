package restore

import (
	"os"
	"path/filepath"
	"syscall"
)

func IsMountpoint(path string) (bool, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(clean)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return false, err
	}
	if os.SameFile(info, parentInfo) {
		return true, nil
	}
	dev, ok := deviceID(info)
	if !ok {
		return false, nil
	}
	parentDev, ok := deviceID(parentInfo)
	if !ok {
		return false, nil
	}
	return dev != parentDev, nil
}

func deviceID(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}
