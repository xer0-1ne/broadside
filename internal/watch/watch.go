// Package watch reports when the content folder changes underneath the server.
//
// This is what makes the flat-file promise real rather than nearly true. A post
// dropped in over Syncthing, pulled down by git, written by a script, or edited
// in a text editor has to appear without anyone restarting the process.
// Otherwise "your content is just files" comes with a footnote, and the
// footnote is the part people run into.
package watch

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher reports filesystem changes, debounced.
type Watcher struct {
	watcher *fsnotify.Watcher
	logger  *slog.Logger

	// roots are the absolute directories being watched, kept so new
	// subdirectories can be checked against them.
	roots []string

	// debounce is how long to wait for quiet before reporting.
	debounce time.Duration

	// onChange runs after the folder has settled.
	onChange func()

	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

// DefaultDebounce is how long to wait for the folder to settle.
//
// Editors do not save a file once. Vim writes a backup, renames, and truncates;
// many editors write to a temporary file and rename over the target; and
// Syncthing writes a temp file, syncs, then renames. A single save can be five
// or six events inside a few milliseconds, and rebuilding the index for each
// one would be pure waste.
//
// Three hundred milliseconds is long enough to collapse a save into one rebuild
// and short enough that a change is on the site before anyone can switch
// windows and reload.
const DefaultDebounce = 300 * time.Millisecond

// Options configures a watcher.
type Options struct {
	// Roots are the directories to watch. Subdirectories are added
	// automatically.
	Roots []string

	Logger   *slog.Logger
	Debounce time.Duration

	// OnChange runs once per settled batch of events.
	OnChange func()
}

// New creates a watcher and begins watching.
func New(opts Options) (*Watcher, error) {
	underlying, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	debounce := opts.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}

	w := &Watcher{
		watcher:  underlying,
		logger:   logger,
		debounce: debounce,
		onChange: opts.OnChange,
	}

	for _, root := range opts.Roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		w.roots = append(w.roots, absolute)
		w.addTree(absolute)
	}

	go w.run()
	return w, nil
}

// addTree watches a directory and everything beneath it.
//
// fsnotify watches a single directory and does not recurse, which matters here
// because the storage layout is sharded by date: every new day is a new
// directory, and a directory nobody is watching is a post nobody notices.
// Walking on startup covers what exists, and the event loop adds new
// directories as they appear.
func (w *Watcher) addTree(root string) {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped rather than failing the whole
			// walk. One bad permission should not stop the rest being watched.
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if shouldIgnore(d.Name()) {
			return filepath.SkipDir
		}
		if err := w.watcher.Add(path); err != nil {
			w.logger.Debug("could not watch directory", "path", path, "error", err)
		}
		return nil
	})
	if err != nil {
		w.logger.Warn("walking watch root", "path", root, "error", err)
	}
}

// shouldIgnore reports whether a name is not worth watching or reacting to.
//
// Version control directories are the important case. A git pull rewrites large
// parts of .git, and watching it would mean a rebuild for every object written
// during a fetch, none of which is a post.
func shouldIgnore(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", ".DS_Store":
		return true
	}
	// Editor swap and backup files, and the temporary files the store itself
	// writes before renaming into place. Reacting to those means rebuilding on
	// the write and again on the rename.
	return strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, "~") ||
		strings.HasSuffix(name, ".swp") ||
		strings.Contains(name, ".tmp-")
}

// run is the event loop.
func (w *Watcher) run() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handle(event)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// An error here is usually the watch limit being hit on Linux,
			// which is worth saying out loud because the symptom is silence:
			// changes simply stop being noticed.
			w.logger.Warn("watch error", "error", err)
		}
	}
}

// handle processes one event.
func (w *Watcher) handle(event fsnotify.Event) {
	name := filepath.Base(event.Name)
	if shouldIgnore(name) {
		return
	}

	// A new directory has to be watched itself, since fsnotify does not
	// recurse. This is what makes the first post of a new day appear: its
	// directory is created, and without this nothing inside it is ever seen.
	//
	// The check is on the event rather than on a stat, because a directory
	// created and populated quickly can be full by the time this runs, so the
	// walk also picks up whatever is already inside it.
	if event.Has(fsnotify.Create) {
		if info, err := filepath.Abs(event.Name); err == nil {
			if isDirectory(info) {
				w.addTree(info)
			}
		}
	}

	// Chmod alone is not a content change. Some tools touch permissions during
	// a sync, and reacting would mean rebuilding for a file whose bytes are
	// identical.
	if event.Op == fsnotify.Chmod {
		return
	}

	w.schedule()
}

// schedule resets the debounce timer.
func (w *Watcher) schedule() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped {
		return
	}

	if w.timer != nil {
		w.timer.Stop()
	}

	// The timer restarts on every event, so a burst of activity produces one
	// callback after it ends rather than one per event. A long-running sync
	// therefore rebuilds once when it finishes, not continuously while it runs.
	w.timer = time.AfterFunc(w.debounce, func() {
		if w.onChange != nil {
			w.onChange()
		}
	})
}

// Close stops watching.
func (w *Watcher) Close() error {
	w.mu.Lock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()

	return w.watcher.Close()
}

// isDirectory reports whether a path is a directory.
//
// A missing path is simply not a directory. Something created and removed again
// before this runs is an ordinary race during a sync, not an error worth
// reporting.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
