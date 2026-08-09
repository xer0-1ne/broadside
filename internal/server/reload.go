package server

import (
	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/index"
)

// Reload rebuilds everything the server holds from what is currently on disk.
//
// This is what the folder watcher calls, and it is deliberately the same path a
// save through the admin takes. Having one reload path means a file arriving
// over Syncthing and a post written in the editor cannot end up in different
// states, which is exactly the kind of divergence that produces a post visible
// through one route and missing from another.
//
// The whole index is rebuilt rather than the changed entry patched. fsnotify
// reports that something happened, not what the result should be, and
// reconstructing the difference from a batch of create, write, rename, and
// remove events is far more delicate than re-reading the folder. At a few
// thousand posts the rebuild is milliseconds.
func (s *Server) Reload() {
	// Config first, so that a timezone change is in effect before the index is
	// built with it. Building the index first would file posts against the old
	// location and then quietly disagree with the settings.
	if cfg, err := config.Load(s.store.Root()); err == nil {
		s.SetConfig(cfg)
	} else {
		s.logger.Warn("reloading config", "error", err)
	}

	rebuilt, problems := index.Build(s.store)
	for _, problem := range problems {
		s.logger.Warn("skipping post during reload", "error", problem)
	}

	s.index.Replace(rebuilt.All())

	// The render cache is keyed by modification time, so an edited post would
	// be noticed on its own. Clearing anyway covers the case the timestamp
	// cannot: a file restored from a backup or a git checkout can carry an
	// older mtime than the copy that was cached, and would otherwise serve the
	// stale render indefinitely.
	s.cache.clear()

	stats := s.index.Stats()
	s.logger.Info("content reloaded",
		"posts", stats.Total,
		"published", stats.Published,
		"drafts", stats.Drafts,
		"scheduled", stats.Scheduled,
	)
}
