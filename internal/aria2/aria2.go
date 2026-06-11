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
