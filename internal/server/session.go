package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/auth"
)

// sessionCookie is the name of the cookie holding the session identifier.
//
// The __Host- prefix is a browser-enforced guarantee, not a convention. A
// cookie with this prefix is only accepted if it is Secure, has no Domain
// attribute, and has Path=/. That means a subdomain cannot set or overwrite it,
// which closes off session fixation from a neighbouring host on the same
// registrable domain.
//
// It requires HTTPS, which is why the plain name is used when the server is
// running without TLS in front of it; see sessionCookieName.
const (
	sessionCookieSecure = "__Host-broadside_session"
	sessionCookiePlain  = "broadside_session"
)

// sessionCookieName picks the cookie name for the current deployment.
//
// A __Host- cookie is silently rejected by the browser over plain HTTP, which
// would make login appear to succeed and then immediately fail. Local
// development over http://localhost is the case that matters, so the prefix is
// used only when the connection is actually secure.
func (s *Server) sessionCookieName(r *http.Request) string {
	if s.isSecure(r) {
		return sessionCookieSecure
	}
	return sessionCookiePlain
}

// isSecure reports whether the reader's connection is over HTTPS.
//
// Behind a proxy the connection to Broadside is plain HTTP, so the only signal
// is the forwarded header, and that is trusted exactly when the operator has
// said a proxy is in front. Trusting it unconditionally would let any client
// claim a secure connection and receive a cookie that then never comes back.
func (s *Server) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.behindProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// setSessionCookie writes the session cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, session *auth.Session) {
	secure := s.isSecure(r)

	http.SetCookie(w, &http.Cookie{
		Name:  s.sessionCookieName(r),
		Value: session.ID,
		Path:  "/",

		// The session identifier must never be readable from JavaScript. This
		// is the single attribute that decides whether an XSS bug is a defaced
		// page or a stolen account, and it is why the session lives in a cookie
		// rather than in localStorage as a JWT.
		HttpOnly: true,

		Secure: secure,

		// Strict rather than Lax. Lax would still send the cookie on a
		// top-level navigation from another site, which is enough for some
		// cross-site request attacks. Nothing on this site needs to be reachable
		// with credentials from an external link, so the stricter setting costs
		// nothing.
		SameSite: http.SameSiteStrictMode,

		Expires: session.Expires,
		MaxAge:  int(time.Until(session.Expires).Seconds()),
	})
}

// clearSessionCookie removes the session cookie on sign-out.
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	// Both names are cleared, because the deployment may have changed between
	// sign-in and sign-out, for example when TLS is put in front of a server
	// that was previously plain.
	for _, name := range []string{sessionCookieSecure, sessionCookiePlain} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   s.isSecure(r),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
	}
}

// currentSession returns the signed-in session for a request, if any.
func (s *Server) currentSession(r *http.Request) (*auth.Session, bool) {
	cookie, err := r.Cookie(s.sessionCookieName(r))
	if err != nil {
		// Fall back to the other name, so an existing session survives a change
		// in whether TLS is present.
		if cookie, err = r.Cookie(sessionCookiePlain); err != nil {
			return nil, false
		}
	}
	return s.authStore.Session(cookie.Value)
}

// isAuthenticated reports whether a request carries a valid session or API
// token.
func (s *Server) isAuthenticated(r *http.Request) bool {
	if _, ok := s.currentSession(r); ok {
		return true
	}
	return s.bearerToken(r) != "" && s.authStore.VerifyToken(s.bearerToken(r))
}

// bearerToken extracts an API token from the Authorization header.
func (s *Server) bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	value, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		return ""
	}
	return strings.TrimSpace(value)
}

// requireSession wraps a handler so only a signed-in browser reaches it.
//
// This is for the admin pages, which are HTML. An unauthenticated request is
// redirected to the login page rather than refused, since a person who followed
// a bookmark should be offered the way in rather than a wall.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentSession(r); !ok {
			// The destination is carried through the login page so the reader
			// lands where they were headed. Only the path is preserved, never a
			// full URL, so this cannot be turned into an open redirect that
			// bounces someone to another site after signing in.
			target := "/login"
			if r.URL.Path != "/admin" {
				target += "?next=" + safeRedirect(r.URL.Path)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireAuth wraps an API handler, accepting either a session or a token.
//
// Unlike requireSession this returns a status code rather than a redirect,
// because the caller is a script and an HTML login page would be nonsense to
// it.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthenticated(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="broadside"`)
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "authentication required",
			})
			return
		}
		next(w, r)
	}
}

// safeRedirect sanitizes a post-login destination.
//
// Only a site-relative path is ever returned. Accepting an arbitrary URL here
// would make the login page an open redirect: an attacker could send someone a
// link to this site's own login form that quietly forwards them to a copy of it
// afterwards, which is a convincing phishing setup precisely because the first
// link is genuine.
//
// The leading double slash is rejected specifically, since "//evil.example" is
// protocol-relative and a browser reads it as another origin despite looking
// like a path.
func safeRedirect(target string) string {
	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return "/admin"
	}
	// A backslash is treated as a separator by some browsers, so "/\evil.com"
	// can escape the origin the same way a double slash does.
	if strings.Contains(target, "\\") {
		return "/admin"
	}
	return target
}

// checkCSRF verifies the token on a state-changing form submission.
//
// SameSite=Strict already stops a cross-site form post in every current
// browser, so this is the second lock rather than the first. It is here because
// the cost is one hidden field and one comparison, and because the entire admin
// write surface sits behind it.
func (s *Server) checkCSRF(r *http.Request, session *auth.Session) bool {
	submitted := r.FormValue("csrf")
	if submitted == "" {
		submitted = r.Header.Get("X-CSRF-Token")
	}
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(session.CSRF)) == 1
}
