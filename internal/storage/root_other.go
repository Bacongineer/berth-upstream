//go:build !linux

package storage

import (
	"errors"
	"os"
)

// Info cannot name a filesystem or measure its space off Linux. The root
// comes back undetectable, which a floor check turns into a warning rather
// than a refusal.
func Info(path string) (Root, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Root{}, err
	}
	if !fi.IsDir() {
		return Root{}, errors.New(path + " is not a directory")
	}
	return Root{Path: path, FS: "unknown"}, nil
}
