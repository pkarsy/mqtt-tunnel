package main

import (
	"fmt"
	"path/filepath"

	"github.com/gofrs/flock"
)

// acquireServerLock attempts to acquire an exclusive lock on the config file.
// Returns the lock handle and error if lock cannot be acquired.
// Caller does NOT need to close the flock - it's released on process exit.
func acquireServerLock(configPath string) (*flock.Flock, error) {
	// Resolve symlinks to lock the real file
	realConfigFile, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		realConfigFile = configPath
	}
	lockFile := realConfigFile + ".lock"

	fl := flock.New(lockFile)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("cannot create lock file %s: %w", lockFile, err)
	}
	if !locked {
		return nil, fmt.Errorf("another server instance is already running with config %s", configPath)
	}

	// Lock is held - it will be released automatically when the process exits
	// (including crash/SIGKILL - OS cleans up file locks)
	return fl, nil
}
