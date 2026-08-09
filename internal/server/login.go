package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/auth"
)

// Login is a page rather than a modal on the public site.
//
// The original design notes called for a modal, and a page is better here for
// three reasons. Password managers and passkey prompts are markedly more
// reliable against a real form on its own document, and passkeys are where this
// is heading. It works with JavaScript disabled, like everything else on the
// site. And it keeps a credential form off every public page, so there is no
// login form for injected content to ever manipulate.
//
// The cost is a URL to know about, and /login is not a hard one to guess.

// handleLoginPage renders the sign-in form.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Someone already signed in has no use for this page.
	if _, ok := s.currentSession(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	data := s.newPageData(r)
	data.Heading = "Sign in"
	data.Next = safeRedirect(r.URL.Query().Get("next"))

	// A site with no password yet shows a setup form instead. That is what
	// makes first run work without a command-line step or a printed
	// credential, which matters most in a container where nobody is watching
	// the logs.
	data.FirstRun = !s.authStore.IsConfigured()
	if data.FirstRun {
		data.Heading = "Create your account"
	}

	s.renderPage(w, r, "login.html", data)
}

// handleLoginSubmit processes the sign-in form.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLoginError(w, r, "That form could not be read.")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	next := safeRedirect(r.FormValue("next"))

	// First run: no password exists, so this request creates the account.
	if !s.authStore.IsConfigured() {
		s.handleFirstRun(w, r, username, password, next)
		return
	}

	// The rate limit is checked before the password is verified, so a blocked
	// address costs nothing beyond a map lookup. Doing the argon2 work first
	// would let an attacker burn 64MB and a tenth of a second per request even
	// while being refused.
	client := s.clientIP(r)
	if !s.rateLimiter.Allow(client) {
		wait := s.rateLimiter.RetryAfter(client)

		s.logger.Warn("login blocked by rate limit", "remote", client, "retry_after", wait)

		w.Header().Set("Retry-After", formatSeconds(wait))
		w.WriteHeader(http.StatusTooManyRequests)

		data := s.newPageData(r)
		data.Heading = "Too many attempts"
		data.Error = "Too many sign-in attempts. Try again in " + humanDuration(wait) + "."
		data.Next = next
		s.renderPage(w, r, "login.html", data)
		return
	}

	session, err := s.authStore.Login(username, password)
	if err != nil {
		s.rateLimiter.Fail(client)

		// The username is logged but the password never is, not even its
		// length. A log file is a place secrets end up living far longer than
		// anybody intended.
		s.logger.Warn("failed login", "username", username, "remote", client)

		if errors.Is(err, auth.ErrInvalidHash) {
			s.logger.Error("stored password hash is unreadable", "error", err)
		}

		// The same message for a wrong username and a wrong password. Telling
		// somebody which half they got right is telling an attacker that half
		// is correct.
		s.renderLoginError(w, r, "That username or password is not right.")
		return
	}

	s.rateLimiter.Reset(client)
	s.setSessionCookie(w, r, session)
	s.logger.Info("signed in", "username", session.Username, "remote", client)

	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleFirstRun creates the account on a site that has none.
//
// This endpoint is only reachable while no password is set, which is what stops
// it being a way to reset somebody else's credentials. The moment an account
// exists, the branch above never runs again.
func (s *Server) handleFirstRun(w http.ResponseWriter, r *http.Request, username, password, next string) {
	if err := s.authStore.SetPassword(username, password); err != nil {
		s.renderLoginError(w, r, capitalize(strings.TrimPrefix(err.Error(), "auth: ")))
		return
	}

	session, err := s.authStore.Login(username, password)
	if err != nil {
		s.renderLoginError(w, r, "The account was created but signing in failed. Try signing in now.")
		return
	}

	s.setSessionCookie(w, r, session)
	s.logger.Info("account created", "username", username, "remote", s.clientIP(r))

	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleLogout ends the session.
//
// This is a POST rather than a GET, which matters more than it looks. A GET
// logout can be triggered by any image tag pointing at it, so a page elsewhere
// on the web could sign the author out of their own site repeatedly, and a
// link prefetcher could do it by accident.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.currentSession(r); ok {
		s.authStore.EndSession(session.ID)
		s.logger.Info("signed out", "username", session.Username)
	}

	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderLoginError redraws the form with a message.
func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, message string) {
	data := s.newPageData(r)
	data.Heading = "Sign in"
	data.Error = message
	data.Next = safeRedirect(r.FormValue("next"))
	data.FirstRun = !s.authStore.IsConfigured()
	if data.FirstRun {
		data.Heading = "Create your account"
	}

	// 401 rather than 200, so that automated tooling and the browser's own
	// password manager can tell a failed attempt from a successful one.
	w.WriteHeader(http.StatusUnauthorized)
	s.renderPage(w, r, "login.html", data)
}

// humanDuration renders a wait in words for the rate limit message.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < 2*time.Minute:
		return "about a minute"
	default:
		return "about " + formatMinutes(d) + " minutes"
	}
}

func formatMinutes(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	return itoa(minutes)
}

func formatSeconds(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return itoa(seconds)
}

// itoa avoids pulling strconv into this file for two call sites.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// capitalize uppercases the first letter of a message for display.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
