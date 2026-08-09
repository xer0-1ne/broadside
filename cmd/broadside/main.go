// Command broadside serves a flat-file blog from a folder of markdown.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// The timezone database, compiled into the binary.
	//
	// Without this, LoadLocation depends on the host having zoneinfo installed,
	// which a FROM scratch container does not. The site would silently fall
	// back to UTC and every post would be filed under the wrong day for anyone
	// west of Greenwich. Roughly 450KB to make a setting work everywhere.
	_ "time/tzdata"

	"git.thebytes.net/roberts/broadside/internal/auth"
	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/content"
	"git.thebytes.net/roberts/broadside/internal/index"
	"git.thebytes.net/roberts/broadside/internal/safepath"
	"git.thebytes.net/roberts/broadside/internal/server"
	"git.thebytes.net/roberts/broadside/internal/watch"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	// All the real work happens in run so that deferred cleanup actually runs.
	// Calling os.Exit from inside main would skip every deferred function,
	// including the ones that close the content directory handle.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "broadside:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		sitePath    = flag.String("site", "./site", "path to the site folder holding posts, uploads, and core")
		listenAddr  = flag.String("listen", "127.0.0.1:8080", "address to listen on")
		behindProxy = flag.Bool("behind-proxy", false, "trust X-Forwarded-* headers from a reverse proxy")
		initSite    = flag.Bool("init", false, "create the site folder structure and a starter post, then exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
		verbose     = flag.Bool("verbose", false, "log at debug level")
		noWatch     = flag.Bool("no-watch", false, "do not watch the site folder for external changes")
	)

	flag.Parse()

	if *showVersion {
		fmt.Println("broadside", version)
		return nil
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// The site directory has to exist before it can be confined, since the
	// whole approach depends on holding an open handle to a real directory.
	absolute, err := filepath.Abs(*sitePath)
	if err != nil {
		return fmt.Errorf("resolving site path: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return fmt.Errorf("creating site folder %s: %w", absolute, err)
	}

	root, err := safepath.Open(absolute)
	if err != nil {
		return fmt.Errorf("opening site folder: %w", err)
	}
	defer root.Close()

	// Every content directory is created up front so that a fresh install has
	// somewhere to put things and an operator browsing the folder can see the
	// shape of it immediately.
	for _, dir := range []string{content.PostsDir, content.UploadsDir, "core", "core/cache", content.RevisionsDir} {
		if err := root.MkdirAll(dir); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// Point temporary files at the site folder rather than the system default.
	//
	// Go spills a multipart upload larger than a few megabytes to a temporary
	// file, and takes that location from the environment. A FROM scratch
	// container has no /tmp at all, so every large upload failed there while
	// working perfectly on a normal host, and reported itself as a size problem
	// rather than a missing directory. Somewhere inside the site volume is
	// guaranteed to exist and be writable, because the server has just created
	// it and cannot run without it.
	uploadTemp := filepath.Join(absolute, "core", "cache", "tmp")
	if err := os.MkdirAll(uploadTemp, 0o755); err == nil {
		os.Setenv("TMPDIR", uploadTemp)

		// Anything left here is debris from an upload interrupted by a crash or
		// a restart, so clearing it at startup keeps it from accumulating
		// silently inside somebody's content folder.
		if entries, err := os.ReadDir(uploadTemp); err == nil {
			for _, entry := range entries {
				os.RemoveAll(filepath.Join(uploadTemp, entry.Name()))
			}
		}
	} else {
		logger.Warn("could not create the upload scratch folder; large uploads may fail",
			"path", uploadTemp, "error", err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		// A malformed config is reported but not fatal, since Load falls back
		// to defaults. Refusing to start would take a site down over a stray
		// comma in a file the operator can fix in a moment.
		logger.Warn("could not read config, using defaults", "error", err)
	}

	// Writing the config back on every start means a fresh site ends up with a
	// complete file showing every available setting and its current value,
	// which is far more discoverable than an empty folder and a manual.
	if err := config.Save(root, cfg); err != nil {
		logger.Warn("could not write config", "error", err)
	}

	if *initSite {
		return initializeSite(root, cfg, logger, absolute)
	}

	store := content.NewStore(root, cfg.Location())

	start := time.Now()
	idx, problems := index.Build(store)

	for _, problem := range problems {
		// A post that failed to load is reported individually. These are almost
		// always a hand-edited file with a malformed header, and naming each
		// one is what lets the author find it.
		logger.Warn("skipping post", "error", problem)
	}

	stats := idx.Stats()
	logger.Info("index built",
		"posts", stats.Total,
		"published", stats.Published,
		"drafts", stats.Drafts,
		"scheduled", stats.Scheduled,
		"skipped", len(problems),
		"took", time.Since(start),
	)

	authStore, err := auth.NewStore(root)
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}
	authStore.SetMinPasswordLength(cfg.MinPasswordLength)
	if !authStore.IsConfigured() {
		// Said once at startup rather than left for somebody to discover. A
		// site with no password is not broken, but the first visitor to reach
		// /login becomes its author, so the operator should know that window is
		// open.
		logger.Warn("no account exists yet; the first visitor to /login will create one")
	}

	srv, err := server.New(server.Options{
		Config:      cfg,
		Store:       store,
		Index:       idx,
		Auth:        authStore,
		Logger:      logger,
		BehindProxy: *behindProxy,
	})
	if err != nil {
		return fmt.Errorf("creating server: %w", err)
	}

	// Watch the content folder so files arriving from outside the process show
	// up without a restart. This is what makes editing by hand, syncing with
	// Syncthing, and pulling from git work as advertised rather than nearly.
	if !*noWatch {
		watcher, err := watch.New(watch.Options{
			// posts and uploads are the content; core carries the config, so a
			// hand-edited setting takes effect the same way a hand-written post
			// does.
			Roots: []string{
				filepath.Join(absolute, content.PostsDir),
				filepath.Join(absolute, content.UploadsDir),
				filepath.Join(absolute, "core"),
			},
			Logger:   logger,
			OnChange: srv.Reload,
		})
		if err != nil {
			// A site that cannot watch its folder still serves it, so this is a
			// warning rather than a failure. The likely cause is an inotify
			// limit, which the message names because the symptom otherwise is
			// simply that changes stop appearing.
			logger.Warn("could not watch the site folder; changes will need a restart",
				"error", err)
		} else {
			defer watcher.Close()
			logger.Info("watching for changes", "site", absolute)
		}
	}

	httpServer := &http.Server{
		Addr:    *listenAddr,
		Handler: srv.Handler(),

		// Timeouts are set explicitly because the defaults are unlimited, which
		// means a single slow or malicious client can hold a connection open
		// forever. On a small VPS that is enough to exhaust the connection
		// pool.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// The server runs in its own goroutine so the main one can wait for a
	// shutdown signal.
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"address", *listenAddr,
			"site", absolute,
			"behind_proxy", *behindProxy,
			"version", version,
		)
		serverErrors <- httpServer.ListenAndServe()
	}()

	// SIGTERM is what a container runtime and systemd send, and SIGINT is
	// Ctrl-C. Handling both means the same shutdown path runs everywhere.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server failed: %w", err)

	case sig := <-shutdown:
		logger.Info("shutting down", "signal", sig.String())

		// In-flight requests are given time to finish rather than being cut
		// off. Fifteen seconds is well past what any page render takes and
		// short enough that a restart is not held up by a stuck connection.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			// Forcing the close is the right response to a shutdown that
			// overran its deadline. The alternative is hanging indefinitely.
			httpServer.Close()
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}

		logger.Info("stopped cleanly")
		return nil
	}
}

// initializeSite writes a starter post so a new install has something to show.
//
// An empty timeline is a poor first impression and gives no example of the file
// format to copy. One post demonstrates the frontmatter, the directives, and
// the folder layout all at once.
func initializeSite(root *safepath.Root, cfg config.Config, logger *slog.Logger, absolute string) error {
	store := content.NewStore(root, cfg.Location())

	post := content.Post{
		Frontmatter: content.Frontmatter{
			Title:     "Hello from Broadside",
			Published: time.Now().In(cfg.Location()),
			Tags:      []string{"broadside"},
			Summary:   "A starter post explaining how this works. Edit it or delete it.",
		},
		Body: starterPostBody,
	}

	path, err := store.Create(post)
	if err != nil {
		return fmt.Errorf("writing the starter post: %w", err)
	}

	logger.Info("site initialized", "path", absolute, "post", path)
	fmt.Printf("\nCreated a site at %s\n\nStart it with:\n  broadside --site %s\n\n", absolute, absolute)
	return nil
}

// starterPostBody is the content of the post written by --init.
const starterPostBody = `This site is a folder of markdown files. There is no database, no build
step, and no admin panel required to publish. Write a file, and it appears.

## Where your writing lives

Posts are stored under ` + "`posts/`" + `, sharded by date:

` + "```" + `
posts/2026/08/08/01-hello-from-broadside.md
` + "```" + `

Sorting those paths alphabetically sorts them chronologically, so a directory
listing is already in timeline order. Splitting by date also keeps any single
folder small enough that you never have to think about it.

## The format

Each file opens with a YAML header and continues as ordinary markdown:

` + "```" + `yaml
---
title: Hello from Broadside
slug: hello-from-broadside
published: 2026-08-08T14:30:22-05:00
tags: [broadside]
---
` + "```" + `

Only ` + "`title`" + `, ` + "`slug`" + `, and ` + "`published`" + ` are required. Any other keys you add are
left exactly as you wrote them, so nothing you put in a header will be quietly
thrown away.

Set ` + "`draft: true`" + ` to keep a post off the timeline. Give ` + "`published`" + ` a future
time and the post stays hidden until that moment arrives.

## Images

An image on its own line becomes a full-width figure that opens in a lightbox
when clicked:

` + "```" + `
![A description for screen readers](/uploads/2026/08/08/01-photo.jpg "An optional caption")
` + "```" + `

Drop the file into ` + "`uploads/`" + ` and reference it by path.

## Editing by hand

Nothing here requires the software to be running. Edit these files in any text
editor, sync them with Syncthing, or keep them in git. Broadside watches the
folder and picks up changes without a restart.

That is the whole idea. Delete this post whenever you are ready.
`
