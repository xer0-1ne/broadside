// Package server serves the public site.
//
// Everything here is server-rendered HTML. A reader with JavaScript disabled
// gets the complete site: every post, every link, every page of the timeline.
// The scripts add a lightbox and infinite scroll on top of markup that already
// works without them, which is what keeps the site readable in a text browser
// and fully visible to a crawler that does not execute anything.
package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/auth"
	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/content"
	"git.thebytes.net/roberts/broadside/internal/index"
	"git.thebytes.net/roberts/broadside/internal/render"
	"git.thebytes.net/roberts/broadside/internal/safepath"
)

// Server holds everything the handlers need.
type Server struct {
	cfg      config.Config
	store    *content.Store
	index    *index.Index
	renderer *render.Renderer
	tmpl     *Templates
	logger   *slog.Logger

	// uploads is confined to the uploads directory rather than to the site
	// root, so a request cannot reach a sibling directory such as core. See
	// handleUpload for the traversal this prevents.
	uploads *safepath.Root

	// cache holds rendered post bodies. It is what makes showing entire posts
	// on the timeline affordable; see postcache.go.
	cache *postCache

	// authStore holds credentials, live sessions, and API tokens.
	authStore *auth.Store

	// rateLimiter throttles the login endpoint. It is per-address and in
	// memory; see the auth package for why that is the right scope here.
	rateLimiter *auth.RateLimiter

	// behindProxy controls whether forwarding headers are trusted. It is off by
	// default because trusting X-Forwarded-For when nothing is in front of the
	// server lets any client claim any address, which would defeat rate
	// limiting later.
	behindProxy bool
}

// Options configures a server.
type Options struct {
	Config      config.Config
	Store       *content.Store
	Index       *index.Index
	Auth        *auth.Store
	Logger      *slog.Logger
	BehindProxy bool
}

// New creates a server.
func New(opts Options) (*Server, error) {
	tmpl, err := LoadTemplates()
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// The uploads directory gets its own confined handle. It is opened once at
	// startup rather than per request, since opening a root is a syscall and
	// the directory does not move.
	uploads, err := opts.Store.Root().Sub(content.UploadsDir)
	if err != nil {
		return nil, fmt.Errorf("server: opening the uploads directory: %w", err)
	}

	return &Server{
		cfg:         opts.Config,
		store:       opts.Store,
		index:       opts.Index,
		renderer:    render.New(),
		tmpl:        tmpl,
		logger:      logger,
		uploads:     uploads,
		cache:       newPostCache(postCacheSize),
		authStore:   opts.Auth,
		rateLimiter: auth.DefaultRateLimiter(),
		behindProxy: opts.BehindProxy,
	}, nil
}

// Config returns the active configuration.
func (s *Server) Config() config.Config { return s.cfg }

// SetConfig replaces the configuration, which the admin uses after a theme
// change so the next request renders with the new colors.
func (s *Server) SetConfig(cfg config.Config) { s.cfg = cfg }

// applyConfig adopts a freshly saved configuration everywhere it is held.
//
// Two places hold it, not one: the server renders from s.cfg, and the auth
// store keeps its own copy of the minimum password length so it can enforce it
// without reaching back here. Anything that saves settings has to update both,
// and the failure mode of updating only the first is that the new minimum
// silently does not apply until the next restart. Having one method for it is
// what stops a second write path from getting that wrong.
func (s *Server) applyConfig(cfg config.Config) {
	s.SetConfig(cfg)
	s.authStore.SetMinPasswordLength(cfg.MinPasswordLength)
}

// Handler builds the complete route table.
//
// Go's ServeMux has handled method and wildcard patterns since 1.22, so a
// third-party router would add a dependency to do what the standard library
// already does. The patterns below are matched by specificity rather than
// registration order, which means the literal routes win over the date pattern
// without any ordering tricks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// The timeline, which is also the search results view and the only route
	// that takes a cursor.
	//
	// There is no /search, /tags, or /archive route any more. Search filters
	// this page in place and a tag link is just a search in tag mode, so those
	// pages had nothing left to show that the timeline does not. Removing them
	// keeps the reader on one page, which is the point of the layout.
	mux.HandleFunc("GET /{$}", s.handleTimeline)

	// JSON Feed only. RSS was removed at the operator's request; this stays
	// because automation picking up new posts is far easier against JSON than
	// against XML, and it costs one handler.
	mux.HandleFunc("GET /feed.json", s.handleJSONFeed)

	// Uploaded media, served through a handler rather than a plain file server
	// so that the content type and disposition are set deliberately. See
	// handleUpload for why that matters.
	mux.HandleFunc("GET /uploads/{path...}", s.handleUpload)

	// The theme as a real stylesheet rather than an inline style block. An
	// inline block would require unsafe-inline in the content security policy,
	// which would defeat the policy's main purpose.
	mux.HandleFunc("GET /theme.css", s.handleThemeCSS)

	// Assets compiled into the binary.
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	// Authentication. A page rather than a modal; see login.go for why.
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)

	// Sign-out is a POST so that an image tag pointing at it cannot sign the
	// author out, and so a link prefetcher cannot do it by accident.
	mux.HandleFunc("POST /logout", s.handleLogout)

	// The admin, four tabs and four routes. Each is bookmarkable and works
	// without JavaScript, which a panel-swapping interface would not.
	mux.HandleFunc("GET /admin", s.requireSession(s.handleAdminContent))
	mux.HandleFunc("GET /admin/media", s.requireSession(s.handleAdminMedia))
	mux.HandleFunc("GET /admin/settings", s.requireSession(s.handleAdminSettings))
	mux.HandleFunc("POST /admin/settings", s.requireSession(s.handleAdminSettingsSave))

	// The account and password forms are separate submissions from the site
	// settings, so that changing a color does not post the password fields
	// alongside it and prompt the browser to update a saved credential.
	mux.HandleFunc("POST /admin/settings/account", s.requireSession(s.handleAdminAccountSave))
	mux.HandleFunc("POST /admin/settings/password", s.requireSession(s.handleAdminPasswordChange))

	// Typefaces and custom social icons the operator has added.
	mux.HandleFunc("POST /admin/settings/font", s.requireSession(s.handleFontUpload))
	mux.HandleFunc("POST /admin/settings/font/delete", s.requireSession(s.handleFontDelete))
	mux.HandleFunc("POST /admin/settings/icon", s.requireSession(s.handleIconUpload))
	mux.HandleFunc("GET /admin/api", s.requireSession(s.handleAdminAPI))
	mux.HandleFunc("POST /admin/api/tokens", s.requireSession(s.handleAdminTokenCreate))
	mux.HandleFunc("POST /admin/api/tokens/revoke", s.requireSession(s.handleAdminTokenRevoke))

	// Post editing.
	mux.HandleFunc("GET /admin/new", s.requireSession(s.handleAdminNewPost))
	mux.HandleFunc("GET /admin/edit/{path...}", s.requireSession(s.handleAdminEditPost))
	mux.HandleFunc("POST /admin/save", s.requireSession(s.handleAdminSavePost))
	mux.HandleFunc("POST /admin/delete", s.requireSession(s.handleAdminDeletePost))

	// Media, shared by the admin form and API clients so the upload rules
	// cannot drift apart between the two.
	mux.HandleFunc("POST /admin/media/upload", s.requireSession(s.handleUploadPost))
	mux.HandleFunc("POST /admin/media/delete", s.requireSession(s.handleMediaDelete))

	// The write API. Everything the admin can do, a script can do, which is
	// what lets n8n or Node-RED drive the site.
	mux.HandleFunc("GET /api/posts", s.requireAuth(s.handleAPIListPosts))
	mux.HandleFunc("GET /api/posts/{slug}", s.requireAuth(s.handleAPIGetPost))
	mux.HandleFunc("POST /api/posts", s.requireAuth(s.handleAPICreatePost))
	mux.HandleFunc("PATCH /api/posts/{slug}", s.requireAuth(s.handleAPIUpdatePost))
	mux.HandleFunc("DELETE /api/posts/{slug}", s.requireAuth(s.handleAPIDeletePost))
	mux.HandleFunc("POST /api/media", s.requireAuth(s.handleUploadPost))
	mux.HandleFunc("GET /api/media", s.requireAuth(s.handleAPIListMedia))

	// Site settings, so a client that can write posts can also change the
	// title and the theme. Credentials are deliberately not reachable here;
	// see settingsapi.go.
	mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleAPIGetSettings))
	mux.HandleFunc("PATCH /api/settings", s.requireAuth(s.handleAPIUpdateSettings))

	// Uploaded typefaces and icons, served from core rather than from the
	// media library, since they are configuration rather than content.
	mux.HandleFunc("GET /fonts/{file}", s.handleServeFont)
	mux.HandleFunc("GET /icons/{file}", s.handleServeIcon)

	// First-run setup. The gate below diverts everything here until the site
	// has been claimed, so these two are the only reachable routes at that
	// point.
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.HandleFunc("POST /setup", s.handleSetupSubmit)

	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /sitemap.xml", s.handleSitemap)
	mux.HandleFunc("GET /health", s.handleHealth)

	// Posts are matched by a catch-all that parses the path itself, rather than
	// by a "/{year}/{month}/{day}/{slug}" pattern.
	//
	// The pattern form looks tidier but does not work here. Four bare wildcards
	// also match "/uploads/2026/08/08", and ServeMux refuses to register two
	// patterns where neither is more specific than the other, so the two routes
	// collide at startup. Every literal prefix above outranks a bare "/", which
	// makes the ambiguity disappear.
	//
	// Parsing by hand also produces better rejections, since a path with the
	// wrong shape becomes a 404 here instead of reaching a handler that has to
	// re-validate it.
	mux.HandleFunc("GET /", s.handlePost)

	return s.withMiddleware(mux)
}

// withMiddleware wraps the router in the cross-cutting concerns.
//
// The order matters. Recovery is outermost so that it catches panics from
// everything inside it, including the logger. Security headers come next so
// they are present even on an error response, which is exactly when a
// vulnerability would otherwise be reachable.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	// The setup gate sits inside the security headers so a setup page still
	// carries them, and outside the router so that every route is covered
	// rather than each one having to remember to check.
	return s.recoverPanics(s.securityHeaders(s.logRequests(s.setupGate(next))))
}

// recoverPanics turns a panic into a 500 rather than a dropped connection.
//
// A panic in one handler should not take the process down. The site is a single
// binary that people run unattended on a small machine, and a crash loop over
// one malformed post would be a poor way to find out something is wrong.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("handler panicked",
					"error", recovered,
					"path", r.URL.Path,
				)
				// The message is deliberately generic. A panic value can
				// contain paths or internal state, and echoing it to a client
				// is how internal details leak.
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the response headers that defend the reader's browser.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	// The policy is built once rather than per request, since it never varies.
	//
	// Every directive here is restrictive by default and widened only where the
	// site genuinely needs it:
	//
	//   default-src 'none'  Nothing loads unless a later directive allows it.
	//                       Starting from nothing means a resource type nobody
	//                       considered is blocked rather than permitted.
	//   script-src 'self'   Only scripts served by this site. No inline script
	//                       and no eval, which is what makes an injected
	//                       <script> tag inert even if one ever got through
	//                       the sanitizer.
	//   style-src 'self'    Same for stylesheets. This is why the theme is a
	//                       route instead of a style block.
	//   img-src 'self' data: blob:
	//                       Uploaded images, plus data and blob URLs so the
	//                       lightbox can work with images the browser has
	//                       already decoded.
	//   frame-ancestors 'none'
	//                       Nobody may frame the site, which is clickjacking
	//                       protection that X-Frame-Options only partly covers.
	policy := strings.Join([]string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data: blob:",
		"media-src 'self'",
		"font-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()

		header.Set("Content-Security-Policy", policy)

		// Stops a browser from second-guessing a declared content type, which
		// is what turns an uploaded text file into executable script.
		header.Set("X-Content-Type-Options", "nosniff")

		// Redundant next to frame-ancestors, but it costs one header and covers
		// browsers that predate CSP level 2.
		header.Set("X-Frame-Options", "DENY")

		// Send the full URL to same-origin requests and only the origin to
		// others, so an external site never learns which post someone came
		// from.
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		next.ServeHTTP(w, r)
	})
}

// logRequests records each request with its status and duration.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start),
			"remote", s.clientIP(r),
		)
	})
}

// statusRecorder captures the status code on its way out, which the
// ResponseWriter interface otherwise does not expose.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// clientIP determines the requesting address, without its port.
//
// Dropping the port is essential rather than cosmetic. RemoteAddr is of the
// form "203.0.113.7:54321", and the port is different on every connection, so
// using it as a rate limit key gives every single attempt its own fresh budget
// and the limiter never fires. That is precisely the bug this stripping fixes,
// and it is invisible in testing unless you actually count the attempts.
//
// X-Forwarded-For is only consulted when the operator has said a proxy is in
// front, because the header is trivially forged by any client. Trusting it
// unconditionally would let an attacker rotate a header value and get an
// unlimited number of login attempts.
func (s *Server) clientIP(r *http.Request) string {
	if s.behindProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// The header is a comma-separated chain, and the leftmost entry is
			// the original client. Later entries are the proxies it passed
			// through.
			if comma := strings.IndexByte(forwarded, ','); comma > 0 {
				return strings.TrimSpace(forwarded[:comma])
			}
			return strings.TrimSpace(forwarded)
		}
	}

	// SplitHostPort fails on an address with no port, which does not happen for
	// a real request but is worth handling rather than returning empty. An
	// empty key would put every client into one shared bucket, which is a
	// different bug in the opposite direction.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// filter returns the visibility rules for a request.
//
// For now every request is public. When authentication lands, this is the one
// place that has to learn about sessions for drafts and scheduled posts to
// become visible to their author across every view at once.
func (s *Server) filter(r *http.Request) index.Filter {
	return index.PublicFilter
}
