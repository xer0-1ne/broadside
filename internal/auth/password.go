// Package auth handles who is allowed to write to the site.
//
// There is exactly one author. That is a product decision rather than a
// limitation to work around, and it keeps this package to a password, a
// session, and a set of API tokens, with no roles, permissions, or membership
// model to get wrong.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password hashing uses argon2id, which is what the current password hashing
// competition winner recommends for this job.
//
// The alternatives were considered and rejected. PBKDF2 and bcrypt are both
// memory-light, which is exactly what makes them cheap to attack on a GPU: an
// attacker with a graphics card can try orders of magnitude more candidates per
// second than the defender's server can. argon2id is deliberately
// memory-hard, so an attacker has to pay for RAM per guess, and RAM is the one
// resource that does not parallelize cheaply.
//
// The id variant is chosen over argon2i or argon2d because it resists both
// side-channel attacks and GPU cracking, rather than picking one.

const (
	// argonTime is the number of passes over memory.
	argonTime = 3

	// argonMemory is the memory cost in KiB, so this is 64MB.
	//
	// This is the parameter that actually does the work. The OWASP guidance of
	// 19MB is a floor for constrained hardware; 64MB is comfortable on
	// anything that can run this binary, including a Raspberry Pi, and costs a
	// login roughly a tenth of a second. Since logins are rare and performed by
	// one person, spending that is free in practice and expensive for an
	// attacker.
	argonMemory = 64 * 1024

	// argonThreads is the parallelism factor.
	argonThreads = 4

	// argonKeyLength is the length of the derived key in bytes.
	argonKeyLength = 32

	// saltLength is the length of the per-password random salt.
	//
	// The salt is what stops one precomputed table from breaking every account
	// using a given password, and stops two identical passwords producing
	// identical hashes.
	saltLength = 16
)

// ErrInvalidHash reports a stored hash that could not be parsed, which means
// the credentials file has been corrupted or hand-edited incorrectly.
var ErrInvalidHash = errors.New("auth: stored password hash is not in the expected format")

// HashPassword derives a storable hash from a plaintext password.
//
// The result carries its own parameters, in the standard PHC string format:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
//
// Encoding the parameters alongside the hash is what makes them changeable
// later. If the cost is raised in a future release, existing hashes still
// verify against the parameters they were created with, and can be upgraded
// quietly on the next successful login.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLength)

	// Raw base64 without padding is what the PHC format specifies.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether a plaintext password matches a stored hash.
//
// A malformed stored hash returns an error rather than false, so that a
// corrupted credentials file is distinguishable from a wrong password in the
// logs. Callers must still show the same message to the user either way.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, fmt.Errorf("auth: hash uses argon2 version %d, this build expects %d: %w",
			version, argon2.Version, ErrInvalidHash)
	}

	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	// The parameters come from the stored hash rather than from the constants
	// above, which is what lets an old hash keep verifying after the cost is
	// raised.
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))

	// A constant-time comparison. A normal byte comparison returns as soon as
	// it finds a difference, and the time it took leaks how many leading bytes
	// were correct, which is enough to recover a hash one byte at a time.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ReleaseMemory returns the memory argon2 just used back to the operating
// system.
//
// Hashing allocates 64MB by design, and Go's runtime holds onto a freed arena
// that large for some minutes in case it is needed again. That is usually the
// right instinct and is wrong here: measured, an idle Broadside sits at about
// eight megabytes, and one login leaves it reporting seventy-six until the
// runtime gets around to releasing it. On the small machines this is built for,
// that difference is the difference between comfortable and worrying, and it
// looks like a leak to anyone watching a graph.
//
// This forces a collection, which is a stop-the-world pause. That is acceptable
// precisely here: a login is rare, it is already rate limited, and the caller
// has just spent a tenth of a second on the hash anyway, so a few extra
// milliseconds are invisible against it.
func ReleaseMemory() {
	debug.FreeOSMemory()
}

// NeedsRehash reports whether a stored hash was made with weaker parameters
// than this build uses.
//
// Calling this after a successful login, and re-hashing when it returns true,
// upgrades everyone's password silently as the cost parameters rise over the
// life of the project. Nobody has to be told to change their password.
func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return true
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return true
	}

	return memory < argonMemory || time < argonTime
}
