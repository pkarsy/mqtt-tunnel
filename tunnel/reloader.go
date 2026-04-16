// Copyright 2026 Panagiotis Karagiannis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tunnel

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigReloader handles file watching and debounced config reloads
type ConfigReloader struct {
	configPath   string // resolved real path (for watching)
	originalPath string // original path as provided (may be symlink)
	debounce     time.Duration
	onReload     func() // callback to trigger reload
	watcher      *fsnotify.Watcher
	timer        *time.Timer
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewConfigReloader creates a new config reloader
func NewConfigReloader(configPath string, debounce time.Duration, onReload func()) (*ConfigReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Resolve symlinks to get the real path
	// This is important because fsnotify watches inodes, and symlinks have different inodes
	realPath, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		// If we can't resolve symlinks (e.g., file doesn't exist), use the original path
		realPath = configPath
	}

	cr := &ConfigReloader{
		configPath:   realPath,
		originalPath: configPath,
		debounce:     debounce,
		onReload:     onReload,
		watcher:      watcher,
		ctx:          ctx,
		cancel:       cancel,
	}

	// Watch the parent directory, not the file itself
	// This handles file replacement (sed -i) which creates a new inode
	configDir := filepath.Dir(realPath)
	if err := watcher.Add(configDir); err != nil {
		watcher.Close()
		cancel()
		return nil, err
	}

	return cr, nil
}

// Start begins watching for file changes
func (cr *ConfigReloader) Start() {
	go cr.watch()
}

// Stop stops the file watcher
func (cr *ConfigReloader) Stop() {
	cr.cancel()
	cr.watcher.Close()
	
	cr.mu.Lock()
	if cr.timer != nil {
		cr.timer.Stop()
	}
	cr.mu.Unlock()
}

func (cr *ConfigReloader) watch() {
	configDir := filepath.Dir(cr.configPath)
	configFile := filepath.Base(cr.configPath)
	debugf("config reloader watching directory: %s for file: %s", configDir, configFile)
	for {
		select {
		case event, ok := <-cr.watcher.Events:
			if !ok {
				debugf("config reloader watcher events channel closed")
				return
			}
			debugf("config reloader event: %s (op: %s)", event.Name, event.Op.String())
			
			// Filter for our specific config file
			if filepath.Base(event.Name) != configFile {
				continue
			}
			
			// Handle Write (normal save) and Create (sed -i replacement)
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if cr.originalPath != cr.configPath {
					log.Printf("[INFO] Config file change detected: %s (symlink to %s)", cr.originalPath, cr.configPath)
				} else {
					log.Printf("[INFO] Config file change detected: %s", event.Name)
				}
				cr.resetTimer()
			}

		case err, ok := <-cr.watcher.Errors:
			if !ok {
				debugf("config reloader watcher errors channel closed")
				return
			}
			log.Printf("[ERROR] Config file watcher error: %v", err)

		case <-cr.ctx.Done():
			debugf("config reloader context cancelled")
			return
		}
	}
}

func (cr *ConfigReloader) resetTimer() {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Stop existing timer if any
	if cr.timer != nil {
		cr.timer.Stop()
	}

	debugf("config reloader debounce timer started: %v", cr.debounce)

	// Create new timer
	cr.timer = time.AfterFunc(cr.debounce, func() {
		debugf("config reloader debounce timer expired, calling onReload")
		cr.onReload()
	})
}
