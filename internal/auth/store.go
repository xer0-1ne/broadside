package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.thebytes.net/roberts/broadside/internal/safepath"
)

// Path is where credentials live, relative to the site root.
const Path = "core/auth.json"

var (
	// ErrNoCredentials reports that nobody has set a password yet, which means
	// the site is waiting for first-run setup.
	ErrNoCredentials = errors.New("auth: no credentials have been set")

	// ErrBadCredentials reports a failed login. It is deliberately the same
	// error for a wrong username and a wrong password, so that the response
	// cannot be used to discover whether a username exists.
	ErrBadCredentials = errors.New("auth: username or password is incorrect")
)

// Credentials is the on-disk contents of core/auth.json.
type Credentials struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`

	// Email is for account recovery and nothing else. It is deliberately not
	// used to sign in: a username that is not an email address is one less
	// thing an attacker can guess from a byline, and this site has no mail
	// delivery to verify an address with anyway.
	Email string `json:"email,omitempty"`

	// Tokens are long-lived API credentials for scripts and the mobile app.
	Tokens []Token `json:"tokens"`
}

// Token is an API credential.
//
// Only the hash is stored. The token itself is shown once, at creation, and
// then cannot be recovered from this file. That is the same model WordPress
// application passwords and the Ghost Admin API use, and it means a leaked
// backup of the site folder does not hand over working API access.
type Token struct {
	// ID identifies the token in the admin list and in revoke requests. It is
	// not secret and is safe to log.
	ID string `json:"id"`

	// Name is what the operator called it, such as "n8n" or "phone".
	Name string `json:"name"`

	// Hash is the SHA-256 of the token, hex encoded.
	//
	// A fast hash is correct here, unlike for passwords. A token is 32 bytes
	// from a cryptographic random source, so there is no dictionary to run
	// against it and nothing for a slow hash to defend. Using argon2 would only
	// make every API request cost 100ms of CPU.
	Hash string `json:"hash"`

	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

// Store holds credentials and the live session set.
//
// Sessions are in memory only. They are deliberately not persisted: a restart
// signing everyone out is barely an inconvenience for a single author, and
// writing session identifiers to disk creates a file that grants site access if
// it ever leaks.
type Store struct {
	root *safepath.Root

	mu          sync.RWMutex
	credentials Credentials
	sessions    map[string]*Session

	// minLength is the configured password floor, kept here so the store can
	// enforce it without reaching into the config package and creating a
	// dependency between the two.
	minLength int
}

// SetMinPasswordLength configures the floor for a new password.
func (s *Store) SetMinPasswordLength(n int) {
	s.mu.Lock()
	s.minLength = n
	s.mu.Unlock()
}

// minPasswordLength returns the floor, with a sane value if none was set.
//
// The fallback is not zero. A store constructed without being configured must
// not silently accept a one-character password, so an unset value means the
// default rather than no rule at all.
func (s *Store) minPasswordLength() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.minLength < 6 {
		return 8
	}
	return s.minLength
}

// Session is one signed-in browser.
type Session struct {
	// ID is the value held in the cookie. It never appears in a URL, so it
	// cannot leak through a referrer header or a shared link.
	ID string

	Username string
	Created  time.Time
	Expires  time.Time

	// CSRF is a per-session token that every state-changing form must echo
	// back. SameSite=Strict on the cookie already blocks the classic
	// cross-site form post, but it is one attribute standing between an
	// attacker and every write endpoint, and browsers have disagreed about its
	// edges before. This is the second lock.
	CSRF string
}

// sessionLifetime is how long a login lasts.
//
// Thirty days, because this is a personal blog and being signed out weekly
// serves nobody. The cookie is the only thing an attacker could steal, and it
// is HttpOnly and SameSite=Strict, so the realistic theft route is physical
// access to an unlocked machine, which a shorter lifetime does not help with
// either.
const sessionLifetime = 30 * 24 * time.Hour

// NewStore loads credentials from the site root.
//
// A missing file is not an error. It means first-run setup has not happened
// yet, which the login page detects and turns into a create-account form.
func NewStore(root *safepath.Root) (*Store, error) {
	store := &Store{
		root:     root,
		sessions: make(map[string]*Session),
	}

	data, err := root.ReadFile(Path)
	if err != nil {
		return store, nil // No credentials yet.
	}

	if err := json.Unmarshal(data, &store.credentials); err != nil {
		return nil, fmt.Errorf("auth: parsing %s: %w", Path, err)
	}

	return store, nil
}

// IsConfigured reports whether a password has been set.
func (s *Store) IsConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.credentials.PasswordHash != ""
}

// Username returns the configured account name.
func (s *Store) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.credentials.Username
}

// Email returns the recovery address.
func (s *Store) Email() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.credentials.Email
}

// SetAccount updates the username and email without touching the password.
//
// Sessions are left alone, unlike a password change. Renaming the account is
// housekeeping rather than a security event, and signing the author out for it
// would be surprising.
func (s *Store) SetAccount(username, email string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("auth: username cannot be empty")
	}

	s.mu.Lock()
	s.credentials.Username = username
	s.credentials.Email = strings.TrimSpace(email)
	s.mu.Unlock()

	return s.save()
}

// ChangePassword replaces the password after verifying the current one.
//
// Requiring the current password is what stops a borrowed session from
// becoming a permanent takeover. Somebody who sits down at an unlocked machine
// can already post, but without this they cannot lock the owner out.
//
// Every other session is ended on success, which is the recovery path when a
// session is believed to be compromised: change the password and everything
// else is signed out.
func (s *Store) ChangePassword(current, next string, keepSessionID string) error {
	s.mu.RLock()
	hash := s.credentials.PasswordHash
	s.mu.RUnlock()

	if hash == "" {
		return ErrNoCredentials
	}

	matches, err := VerifyPassword(current, hash)
	if err != nil {
		return err
	}
	if !matches {
		return ErrBadCredentials
	}

	if len(next) < s.minPasswordLength() {
		return fmt.Errorf("auth: the new password must be at least %d characters", s.minPasswordLength())
	}
	if next == current {
		return errors.New("auth: the new password must be different from the current one")
	}

	updated, err := HashPassword(next)
	if err != nil {
		return err
	}
	defer ReleaseMemory()

	s.mu.Lock()
	s.credentials.PasswordHash = updated

	// Every session except the one making the change is dropped. Ending the
	// current one too would sign the author out of the page they are standing
	// on, which reads as a failure rather than as a security measure.
	for id := range s.sessions {
		if id != keepSessionID {
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()

	return s.save()
}

// SetPassword creates or replaces the account.
//
// Every existing session is dropped, because a password change is either the
// operator securing the account or recovering from a compromise, and in both
// cases any session already open should stop working.
func (s *Store) SetPassword(username, password string) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("auth: username cannot be empty")
	}
	if len(password) < s.minPasswordLength() {
		return fmt.Errorf("auth: password must be at least %d characters", s.minPasswordLength())
	}

	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	defer ReleaseMemory()

	s.mu.Lock()
	s.credentials.Username = strings.TrimSpace(username)
	s.credentials.PasswordHash = hash
	clear(s.sessions)
	s.mu.Unlock()

	return s.save()
}

// Login verifies credentials and returns a new session.
func (s *Store) Login(username, password string) (*Session, error) {
	s.mu.RLock()
	credentials := s.credentials
	s.mu.RUnlock()

	if credentials.PasswordHash == "" {
		return nil, ErrNoCredentials
	}

	// The password is verified even when the username is already wrong.
	//
	// Returning early on a bad username would make that case measurably faster
	// than a bad password, and the difference is enough to enumerate valid
	// usernames by timing alone. Doing the work regardless keeps both paths the
	// same length.
	usernameMatches := subtle.ConstantTimeCompare(
		[]byte(strings.ToLower(strings.TrimSpace(username))),
		[]byte(strings.ToLower(credentials.Username)),
	) == 1

	passwordMatches, err := VerifyPassword(password, credentials.PasswordHash)
	if err != nil {
		return nil, err
	}

	// Whether the attempt succeeded or not, the hashing arena is no longer
	// needed. Releasing on failure matters more than on success, since a
	// stream of failed attempts is exactly when the memory would otherwise
	// accumulate.
	defer ReleaseMemory()

	if !usernameMatches || !passwordMatches {
		return nil, ErrBadCredentials
	}

	// Upgrade the stored hash if this build uses stronger parameters than the
	// one that created it. This is the only moment the plaintext is available.
	if NeedsRehash(credentials.PasswordHash) {
		if upgraded, err := HashPassword(password); err == nil {
			s.mu.Lock()
			s.credentials.PasswordHash = upgraded
			s.mu.Unlock()
			s.save()
		}
	}

	return s.newSession(credentials.Username)
}

// newSession mints a session and records it.
func (s *Store) newSession(username string) (*Session, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}

	session := &Session{
		ID:       id,
		Username: username,
		Created:  time.Now(),
		Expires:  time.Now().Add(sessionLifetime),
		CSRF:     csrf,
	}

	s.mu.Lock()
	s.sessions[id] = session
	s.pruneSessionsLocked()
	s.mu.Unlock()

	return session, nil
}

// pruneSessionsLocked drops expired sessions. The caller holds the write lock.
//
// Sweeping on creation rather than on a timer means there is no background
// goroutine to manage, and the map cannot grow without bound because the only
// thing that adds to it also cleans it.
func (s *Store) pruneSessionsLocked() {
	now := time.Now()
	for id, session := range s.sessions {
		if now.After(session.Expires) {
			delete(s.sessions, id)
		}
	}
}

// Session looks up a live session by its identifier.
func (s *Store) Session(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}

	s.mu.RLock()
	session, found := s.sessions[id]
	s.mu.RUnlock()

	if !found || time.Now().After(session.Expires) {
		return nil, false
	}
	return session, true
}

// EndSession signs one browser out.
func (s *Store) EndSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// CreateToken mints an API token and returns the secret.
//
// The secret is returned here and never again. It is not stored, only its hash
// is, so an operator who loses it has to issue a new one. That is the point:
// the alternative is a file on disk that grants full write access to the site.
func (s *Store) CreateToken(name string) (secret string, token Token, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unnamed token"
	}

	secret, err = randomToken()
	if err != nil {
		return "", Token{}, err
	}

	id, err := randomID()
	if err != nil {
		return "", Token{}, err
	}

	token = Token{
		ID:      id,
		Name:    name,
		Hash:    hashToken(secret),
		Created: time.Now(),
	}

	s.mu.Lock()
	s.credentials.Tokens = append(s.credentials.Tokens, token)
	s.mu.Unlock()

	if err := s.save(); err != nil {
		return "", Token{}, err
	}
	return secret, token, nil
}

// VerifyToken checks an API token and records its use.
func (s *Store) VerifyToken(secret string) bool {
	if secret == "" {
		return false
	}
	candidate := hashToken(secret)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.credentials.Tokens {
		// Constant time, for the same reason the password comparison is: a
		// byte-by-byte comparison leaks how much of the hash was correct.
		if subtle.ConstantTimeCompare([]byte(s.credentials.Tokens[i].Hash), []byte(candidate)) == 1 {
			now := time.Now()
			s.credentials.Tokens[i].LastUsed = &now

			// Saved outside the lock would race with another request; saved
			// inside it, on every request, would write to disk constantly. The
			// timestamp is only for the admin list, so a lost update on a
			// crash costs nothing and it is flushed lazily by the next
			// credential change.
			return true
		}
	}
	return false
}

// Tokens lists the issued API tokens, without their secrets.
func (s *Store) Tokens() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tokens := make([]Token, len(s.credentials.Tokens))
	copy(tokens, s.credentials.Tokens)
	return tokens
}

// RevokeToken deletes a token by its identifier.
func (s *Store) RevokeToken(id string) error {
	s.mu.Lock()
	kept := s.credentials.Tokens[:0]
	for _, token := range s.credentials.Tokens {
		if token.ID != id {
			kept = append(kept, token)
		}
	}
	s.credentials.Tokens = kept
	s.mu.Unlock()

	return s.save()
}

// save writes credentials to disk.
func (s *Store) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.credentials, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("auth: encoding credentials: %w", err)
	}
	data = append(data, '\n')

	f, err := s.root.Create(Path)
	if err != nil {
		return fmt.Errorf("auth: writing %s: %w", Path, err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("auth: writing %s: %w", Path, err)
	}
	return f.Sync()
}

// randomToken returns 32 bytes of cryptographic randomness, URL-safe encoded.
//
// crypto/rand is the only acceptable source here. math/rand is seeded
// predictably and its output can be reconstructed from a few samples, which
// for a session identifier means an attacker can mint their own.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// randomID returns a short identifier for naming a token in the UI.
func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// hashToken derives the stored form of an API token.
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
