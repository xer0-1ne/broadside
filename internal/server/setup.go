package server

import (
	"net/http"
	"strings"

	"git.thebytes.net/roberts/broadside/internal/config"
)

// Until setup is complete, the entire site answers with the setup page.
//
// The obvious alternative is to serve the blog publicly and only guard the
// admin, and that is worse for a reason worth stating plainly: between first
// boot and somebody claiming the site, whoever finds it can claim it. A
// container that gets port-forwarded before its owner sits down to configure
// it is an ordinary situation, not a contrived one, and the window is measured
// in however long they take to get to it.
//
// Gating everything closes that window instead of documenting it. The cost is
// that a fresh site shows a setup form rather than an empty timeline, which is
// more useful anyway.

// setupGate wraps the whole router, diverting everything to setup until an
// account exists.
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isSetUp() {
			next.ServeHTTP(w, r)
			return
		}

		// The stylesheet, the theme, and the fonts have to load or the setup
		// page arrives unstyled. These are static and reveal nothing about a
		// site that has no content yet.
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/theme.css" {
			next.ServeHTTP(w, r)
			return
		}

		switch {
		case r.URL.Path == "/setup" && r.Method == http.MethodGet:
			s.handleSetupPage(w, r)
		case r.URL.Path == "/setup" && r.Method == http.MethodPost:
			s.handleSetupSubmit(w, r)
		default:
			// Everything else is redirected rather than answered. A 404 would
			// leave somebody wondering whether the site was broken; landing on
			// the form tells them exactly what is needed.
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
		}
	})
}

// isSetUp reports whether the site has been claimed.
//
// Both halves have to be true. The config flag is what the operator sees and
// can inspect, and the credential check is what actually matters, so a
// hand-edited config that flips the flag without an account still lands on
// setup rather than leaving an unprotected admin.
func (s *Server) isSetUp() bool {
	return s.cfg.SetupComplete && s.authStore.IsConfigured()
}

// handleSetupPage renders the first-run form.
//
// Once the site is claimed this is no longer a page, and reaching it sends the
// visitor to the sign-in form instead. The submit handler already refuses to
// act after setup, so this is not the lock; it stops a claimed site from
// showing a create-account form to anyone who guesses the URL, which at best
// is confusing and at worst invites somebody to try filling it in.
func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	if s.isSetUp() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := s.newPageData(r)
	data.Heading = "Set up your site"
	data.FirstRun = true

	s.renderPage(w, r, "setup.html", data)
}

// handleSetupSubmit creates the account and unlocks the site.
func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSetupError(w, r, "That form could not be read.")
		return
	}

	// Reachable only while the site is unclaimed, which is what stops this from
	// being a way to reset somebody else's credentials. The gate above never
	// routes here once setup is done.
	if s.isSetUp() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		s.renderSetupError(w, r, "Those two passwords do not match.")
		return
	}

	if err := s.authStore.SetPassword(username, password); err != nil {
		s.renderSetupError(w, r, capitalize(strings.TrimPrefix(err.Error(), "auth: ")))
		return
	}

	if email := strings.TrimSpace(r.FormValue("email")); email != "" {
		s.authStore.SetAccount(username, email)
	}

	// The site's own details are collected on the same form, because a person
	// setting up a blog is already thinking about what to call it, and sending
	// them to a settings page afterwards to name it is a needless second step.
	cfg := s.cfg
	if title := strings.TrimSpace(r.FormValue("title")); title != "" {
		cfg.Title = title
	}
	if slogan := strings.TrimSpace(r.FormValue("slogan")); slogan != "" {
		cfg.Slogan = slogan
	}
	if displayName := strings.TrimSpace(r.FormValue("display_name")); displayName != "" {
		cfg.DisplayName = displayName
	} else {
		cfg.DisplayName = username
	}

	// The flag flips last, after everything that could fail has succeeded. If
	// the save below fails, the site stays on setup rather than unlocking with
	// no record of what was configured.
	cfg.SetupComplete = true

	if err := config.Save(s.store.Root(), cfg); err != nil {
		s.logger.Error("saving config during setup", "error", err)
		s.renderSetupError(w, r, "The settings could not be written. Check that the site folder is writable.")
		return
	}
	s.SetConfig(cfg)

	session, err := s.authStore.Login(username, password)
	if err != nil {
		// The account exists, so sending them to sign in normally is a working
		// path rather than a dead end.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	s.setSessionCookie(w, r, session)
	s.logger.Info("site set up", "username", username)

	http.Redirect(w, r, "/admin/settings?notice=Your+site+is+ready", http.StatusSeeOther)
}

// renderSetupError redraws the setup form with a message.
func (s *Server) renderSetupError(w http.ResponseWriter, r *http.Request, message string) {
	data := s.newPageData(r)
	data.Heading = "Set up your site"
	data.FirstRun = true
	data.Error = message

	w.WriteHeader(http.StatusBadRequest)
	s.renderPage(w, r, "setup.html", data)
}
