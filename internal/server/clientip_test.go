package server

import (
	"net/http/httptest"
	"testing"
)

// TestClientIPDropsThePort pins a bug that made the login rate limiter
// completely ineffective while looking entirely correct.
//
// RemoteAddr is "host:port", and the port is different on every connection. The
// limiter keys on whatever this returns, so returning the address unchanged
// gave every single login attempt its own fresh budget and the limit never
// fired. Nothing about the limiter itself was wrong, and no test of the limiter
// in isolation would have caught it.
func TestClientIPDropsThePort(t *testing.T) {
	s := &Server{}

	cases := []struct {
		remote string
		want   string
	}{
		{"203.0.113.7:54321", "203.0.113.7"},
		{"203.0.113.7:54322", "203.0.113.7"},
		{"127.0.0.1:8080", "127.0.0.1"},
		// IPv6 is bracketed in RemoteAddr, and SplitHostPort unwraps it.
		{"[2001:db8::1]:443", "2001:db8::1"},
	}

	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = tc.remote

		if got := s.clientIP(r); got != tc.want {
			t.Errorf("clientIP(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// TestClientIPGivesTwoConnectionsFromOneHostTheSameKey states the property that
// actually matters, rather than the implementation detail above.
func TestClientIPGivesTwoConnectionsFromOneHostTheSameKey(t *testing.T) {
	s := &Server{}

	first := httptest.NewRequest("POST", "/login", nil)
	first.RemoteAddr = "203.0.113.7:11111"

	second := httptest.NewRequest("POST", "/login", nil)
	second.RemoteAddr = "203.0.113.7:22222"

	if s.clientIP(first) != s.clientIP(second) {
		t.Error("two connections from one address produced different rate limit keys, so the limiter would never fire")
	}
}

// TestForwardedHeaderIsIgnoredWithoutAProxy covers the other half. The header is
// trivially forged, so trusting it when nothing is in front of the server would
// let an attacker rotate its value and get unlimited login attempts.
func TestForwardedHeaderIsIgnoredWithoutAProxy(t *testing.T) {
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.99")

	direct := &Server{behindProxy: false}
	if got := direct.clientIP(r); got != "203.0.113.7" {
		t.Errorf("got %q, want the real address when no proxy is configured", got)
	}

	proxied := &Server{behindProxy: true}
	if got := proxied.clientIP(r); got != "198.51.100.99" {
		t.Errorf("got %q, want the forwarded address when a proxy is configured", got)
	}
}

// TestForwardedChainTakesTheOriginalClient checks the multi-hop form, where the
// leftmost entry is the original client and the rest are proxies.
func TestForwardedChainTakesTheOriginalClient(t *testing.T) {
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.99, 10.0.0.5, 10.0.0.6")

	s := &Server{behindProxy: true}
	if got := s.clientIP(r); got != "198.51.100.99" {
		t.Errorf("got %q, want the leftmost entry of the chain", got)
	}
}

// TestSafeRedirectRefusesOtherOrigins keeps the login page from becoming an open
// redirect, which would make a genuine link to this site a convincing way to
// land somebody on a copy of it.
func TestSafeRedirectRefusesOtherOrigins(t *testing.T) {
	hostile := []string{
		"//evil.example",
		"https://evil.example",
		"http://evil.example",
		`/\evil.example`,
		`\\evil.example`,
		"",
		"evil.example",
	}

	for _, target := range hostile {
		if got := safeRedirect(target); got != "/admin" {
			t.Errorf("safeRedirect(%q) = %q, want it refused back to /admin", target, got)
		}
	}

	// A genuine site-relative path is preserved, or the feature does nothing.
	for _, target := range []string{"/admin/settings", "/admin/api", "/admin/media"} {
		if got := safeRedirect(target); got != target {
			t.Errorf("safeRedirect(%q) = %q, want it kept", target, got)
		}
	}
}
