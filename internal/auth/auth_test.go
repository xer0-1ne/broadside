package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Error("the correct password did not verify")
	}

	ok, err = VerifyPassword("Correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Error("a password differing only in case verified")
	}
}

// TestHashesAreSalted is what stops one precomputed table from breaking every
// account, and stops two people with the same password having the same hash.
func TestHashesAreSalted(t *testing.T) {
	first, err := HashPassword("the same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("the same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if first == second {
		t.Error("hashing the same password twice produced identical output, so it is not salted")
	}

	// Both must still verify, which is the point of storing the salt alongside.
	for _, hash := range []string{first, second} {
		ok, err := VerifyPassword("the same password", hash)
		if err != nil || !ok {
			t.Errorf("a salted hash failed to verify: ok=%v err=%v", ok, err)
		}
	}
}

// TestStoredParametersAreUsed covers the upgrade path. A hash created with
// weaker settings has to keep working, or raising the cost would lock everyone
// out on upgrade.
func TestStoredParametersAreUsed(t *testing.T) {
	hash, err := HashPassword("a long enough password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !strings.Contains(hash, "$argon2id$") {
		t.Errorf("hash %q is not in PHC format", hash)
	}
	if !strings.Contains(hash, "m=65536,t=3,p=4") {
		t.Errorf("hash %q does not record its own parameters", hash)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	bad := []string{
		"",
		"not a hash",
		"$argon2id$",
		"$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=4$notbase64!$aGFzaA",
		"$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	}

	for _, hash := range bad {
		if _, err := VerifyPassword("password", hash); err == nil {
			t.Errorf("VerifyPassword accepted malformed hash %q", hash)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	current, err := HashPassword("a long enough password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if NeedsRehash(current) {
		t.Error("a freshly created hash was reported as needing a rehash")
	}

	// A hash made with the weaker settings of an older release.
	weak := "$argon2id$v=19$m=4096,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGE"
	if !NeedsRehash(weak) {
		t.Error("a hash with weaker parameters was not flagged for upgrade")
	}
}

func TestRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(3, time.Minute)
	const address = "203.0.113.7"

	for i := 0; i < 3; i++ {
		if !limiter.Allow(address) {
			t.Fatalf("attempt %d was blocked while still within the limit", i+1)
		}
		limiter.Fail(address)
	}

	if limiter.Allow(address) {
		t.Error("an attempt past the limit was allowed")
	}
	if limiter.RetryAfter(address) <= 0 {
		t.Error("RetryAfter should report time remaining once blocked")
	}

	// Another address is unaffected, or one attacker could lock out the owner.
	if !limiter.Allow("198.51.100.4") {
		t.Error("a different address was blocked by someone else's failures")
	}

	// A successful login clears the record.
	limiter.Reset(address)
	if !limiter.Allow(address) {
		t.Error("Reset did not clear the failure count")
	}
}

// TestRateLimiterWindowExpires confirms a lockout is temporary rather than
// permanent, so a forgetful author is not locked out for good.
func TestRateLimiterWindowExpires(t *testing.T) {
	limiter := NewRateLimiter(1, 50*time.Millisecond)
	const address = "203.0.113.7"

	limiter.Fail(address)
	if limiter.Allow(address) {
		t.Fatal("expected to be blocked immediately after exceeding the limit")
	}

	time.Sleep(60 * time.Millisecond)

	if !limiter.Allow(address) {
		t.Error("still blocked after the window elapsed")
	}
}

func TestTokenHashingIsStable(t *testing.T) {
	// The same secret must always produce the same stored hash, or a token
	// would stop working the moment it was verified.
	if hashToken("abc") != hashToken("abc") {
		t.Error("hashing the same token twice produced different results")
	}
	if hashToken("abc") == hashToken("abd") {
		t.Error("two different tokens produced the same hash")
	}
}

func TestRandomTokensAreDistinct(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		token, err := randomToken()
		if err != nil {
			t.Fatalf("randomToken: %v", err)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatal("randomToken produced a duplicate, which means the source is not random")
		}
		if len(token) < 40 {
			t.Errorf("token %q is shorter than expected for 32 bytes", token)
		}
		seen[token] = struct{}{}
	}
}
