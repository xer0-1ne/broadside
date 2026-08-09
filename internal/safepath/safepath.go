// Package safepath confines filesystem access to a single directory tree.
//
// Path traversal is the most common way flat file publishing platforms get
// compromised, because nearly every request carries a slug, a filename, or an
// upload path that ultimately becomes part of a real path on disk. The usual
// defense is to inspect the untrusted string and reject anything suspicious,
// which fails often enough to be considered a losing strategy. Attackers have
// URL encoding, double encoding, overlong UTF-8, backslashes on Windows,
// NUL bytes, and symlinks to work with, and a check that covers all of those
// today tends not to cover the one added next year.
//
// This package takes the other approach. Instead of judging paths, it opens the
// content directory once and performs every subsequent operation relative to
// that open handle using os.Root, which was added in Go 1.24. The kernel
// resolves each path component against the directory, and any component that
// would escape the tree fails at the syscall rather than passing a check that
// was not clever enough. A traversal attempt is not something to detect. It is
// something the operation cannot express.
//
// Symlinks deserve a specific note, because they are the case that defeats
// string inspection entirely. A path such as posts/evil.md contains nothing
// suspicious, but if evil.md is a symlink to /etc/passwd, then validating the
// string and opening the file are two different questions with two different
// answers. os.Root refuses to traverse a symlink whose target lands outside the
// root, so the gap between what was checked and what was opened closes.
//
// One consequence is worth knowing before it arrives as a bug report. Inside a
// root, an absolute symlink target is interpreted relative to the root rather
// than to the real filesystem, so a link pointing at /var/site/posts/real.md is
// read as root + /var/site/posts/real.md and refused even though the target sits
// inside the content folder. Honoring absolute targets would be a trivial
// escape, so this is the correct behavior, but it means anyone linking files
// together by hand has to use relative targets.
package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrEscapes reports that a path resolved outside the root it was checked
// against. Callers that surface filesystem errors to users should treat this as
// a flat rejection and avoid echoing the offending path back, since repeating
// attacker-controlled input into a response is its own small problem.
var ErrEscapes = errors.New("safepath: path escapes the root directory")

// Root is a directory that filesystem operations are confined to. The zero
// value is not usable; construct one with Open. A Root is safe for concurrent
// use, matching the behavior of the *os.Root it wraps.
type Root struct {
	root *os.Root

	// abs is the resolved absolute path of the root, kept for the Resolve
	// fallback and for error messages. Symlinks are already expanded here so
	// that prefix comparisons in Resolve compare like with like.
	abs string
}

// Open confines subsequent operations to dir, which must already exist and be a
// directory.
//
// The path is resolved through filepath.EvalSymlinks first. On macOS in
// particular the common temporary directory is itself a symlink, so a root
// opened at /tmp/x and a file later resolved to /private/tmp/x would compare
// unequal despite being the same location. Resolving once here means the
// comparison in Resolve is between two fully expanded paths.
func Open(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("safepath: resolving %q: %w", dir, err)
	}

	// EvalSymlinks fails if the directory does not exist, which is a legitimate
	// error worth reporting rather than papering over. A root that does not
	// exist cannot confine anything.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("safepath: resolving symlinks in %q: %w", abs, err)
	}

	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("safepath: opening root %q: %w", resolved, err)
	}

	return &Root{root: root, abs: resolved}, nil
}

// Path returns the absolute, symlink-resolved location of the root. It is
// intended for log lines and error messages rather than for building paths,
// since anything built from it would bypass the confinement this package exists
// to provide.
func (r *Root) Path() string { return r.abs }

// Sub returns a Root confined to a subdirectory of this one.
//
// Use this whenever untrusted input names a file inside a specific
// subdirectory, and never build such a path with path.Join first. The
// distinction is not stylistic, and it is worth spelling out because getting it
// wrong produces a working traversal against code that looks careful.
//
// A Root confines to the directory it was opened at. Joining an untrusted
// segment onto a prefix before handing it over resolves any ".." against that
// prefix rather than against the root, so "uploads" plus "../core/config.json"
// becomes "core/config.json": a clean path, containing no traversal, pointing
// somewhere the caller never intended. The Root then accepts it, correctly,
// because that path really is inside the root it is guarding.
//
// Requesting a sub-root moves the boundary to where the caller actually meant
// it. The same input is then rejected outright, because ".." genuinely does
// lead above the uploads directory.
//
// Note also that http.Request.PathValue returns a decoded path, so a request
// for "%2e%2e" arrives here as "..". Percent-encoding does not survive to this
// layer and cannot be relied on to have been screened earlier.
func (r *Root) Sub(dir string) (*Root, error) {
	cleaned, err := clean(dir)
	if err != nil {
		return nil, err
	}

	sub, err := r.root.OpenRoot(cleaned)
	if err != nil {
		return nil, fmt.Errorf("safepath: opening sub-root %q: %w", cleaned, err)
	}

	// Resolve the absolute path through the parent so that symlinks are
	// expanded the same way they were when the parent was opened. Resolve
	// cannot escape, so this cannot widen the confinement.
	abs, err := r.Resolve(cleaned)
	if err != nil {
		sub.Close()
		return nil, err
	}

	return &Root{root: sub, abs: abs}, nil
}

// Close releases the underlying directory handle.
func (r *Root) Close() error { return r.root.Close() }

// clean normalizes an untrusted relative path into the slash-separated form
// os.Root expects.
//
// This is deliberately not a security check. os.Root is what makes traversal
// impossible, and nothing here is load bearing in that sense. The work done
// below is normalization so that equivalent paths take the same form, plus two
// cheap rejections for input that is malformed rather than merely unusual.
func clean(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("safepath: empty path: %w", ErrEscapes)
	}

	// A NUL byte truncates the path at the syscall boundary in some C
	// libraries, which historically allowed "safe.txt\x00.php" to pass a suffix
	// check and then be written as a different file entirely. Go's own syscall
	// layer rejects these, so this is defense in depth rather than a live hole,
	// but the check costs a scan of a short string.
	if strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("safepath: path contains a NUL byte: %w", ErrEscapes)
	}

	// Windows accepts backslash as a separator, so an attacker on that platform
	// could otherwise write "..\\.." and have it treated as one opaque path
	// element here while the OS read it as two levels up. Normalizing to
	// forward slashes on every platform means the same input produces the same
	// result regardless of where the binary runs.
	name = strings.ReplaceAll(name, `\`, "/")

	// path.Clean collapses "." and ".." lexically and removes duplicate
	// separators. An absolute path becomes rooted at "/" here, which the check
	// below then rejects.
	cleaned := path.Clean(name)

	switch {
	case cleaned == ".":
		// The caller asked for the root itself using a relative path. That is
		// not a file operation, so reject it rather than guess an intent.
		return "", fmt.Errorf("safepath: path refers to the root itself: %w", ErrEscapes)
	case path.IsAbs(cleaned):
		// os.Root rejects absolute paths on its own. Catching it here produces
		// an error that says what actually went wrong.
		return "", fmt.Errorf("safepath: absolute paths are not accepted: %w", ErrEscapes)
	case cleaned == ".." || strings.HasPrefix(cleaned, "../"):
		// Surviving path.Clean as a leading ".." means the path genuinely
		// pointed above the root rather than merely containing dot-dot in the
		// middle. os.Root would refuse this too; the early return just makes
		// the reason legible.
		return "", fmt.Errorf("safepath: path leads above the root: %w", ErrEscapes)
	}

	return cleaned, nil
}

// Open opens a file for reading, relative to the root.
func (r *Root) Open(name string) (*os.File, error) {
	cleaned, err := clean(name)
	if err != nil {
		return nil, err
	}
	return r.root.Open(cleaned)
}

// ReadFile reads an entire file relative to the root.
//
// This exists rather than sending callers to os.ReadFile because the whole
// point of the package is that no caller should ever hold a path it could pass
// to the os package directly.
func (r *Root) ReadFile(name string) ([]byte, error) {
	cleaned, err := clean(name)
	if err != nil {
		return nil, err
	}
	return r.root.ReadFile(cleaned)
}

// Create opens a file for writing relative to the root, truncating it if it
// already exists and creating any parent directories that are missing.
//
// Creating the parents is a convenience the date-sharded layout genuinely
// needs, since the first post of any given day lands in a directory tree that
// does not exist yet. Those directories are created through the root as well,
// so a symlink planted partway down cannot redirect the write elsewhere.
func (r *Root) Create(name string) (*os.File, error) {
	cleaned, err := clean(name)
	if err != nil {
		return nil, err
	}
	if err := r.makeParents(cleaned); err != nil {
		return nil, err
	}
	return r.root.Create(cleaned)
}

// makeParents creates the parent directories of an already-cleaned path.
func (r *Root) makeParents(cleaned string) error {
	dir := path.Dir(cleaned)
	if dir == "." || dir == "/" {
		return nil // The file sits directly in the root, so there is nothing to create.
	}
	// An existing directory is the expected case for every post after the first
	// one on a given day, and MkdirAll treats that as success, so there is no
	// reason to stat first.
	if err := r.root.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("safepath: creating directory %q: %w", dir, err)
	}
	return nil
}

// MkdirAll creates a directory and any missing parents, relative to the root.
func (r *Root) MkdirAll(name string) error {
	cleaned, err := clean(name)
	if err != nil {
		return err
	}
	if err := r.root.MkdirAll(cleaned, 0o755); err != nil {
		return fmt.Errorf("safepath: creating directory %q: %w", cleaned, err)
	}
	return nil
}

// Stat returns file information for a path relative to the root.
func (r *Root) Stat(name string) (fs.FileInfo, error) {
	cleaned, err := clean(name)
	if err != nil {
		return nil, err
	}
	return r.root.Stat(cleaned)
}

// Exists reports whether a path exists relative to the root.
//
// A path that escapes the root reports false rather than returning an error,
// because callers asking this question are choosing between branches and an
// unreachable path is correctly described as absent.
func (r *Root) Exists(name string) bool {
	_, err := r.Stat(name)
	return err == nil
}

// Remove deletes a single file relative to the root.
func (r *Root) Remove(name string) error {
	cleaned, err := clean(name)
	if err != nil {
		return err
	}
	return r.root.Remove(cleaned)
}

// Rename moves a file within the root.
//
// Both operands are resolved against the root, so neither the source nor the
// destination can point outside it. This is what makes the write-then-rename
// pattern in the store safe to use with names that came from a request.
func (r *Root) Rename(oldName, newName string) error {
	oldCleaned, err := clean(oldName)
	if err != nil {
		return err
	}
	newCleaned, err := clean(newName)
	if err != nil {
		return err
	}
	if err := r.makeParents(newCleaned); err != nil {
		return err
	}
	return r.root.Rename(oldCleaned, newCleaned)
}

// FS returns a read-only fs.FS view of the root.
//
// This is what makes fs.WalkDir usable for building the post index. The walk
// stays inside the root, and because os.Root does not follow symlinks out of
// the tree, a symlink dropped into the posts folder cannot pull unrelated files
// into the index.
func (r *Root) FS() fs.FS { return r.root.FS() }

// Resolve converts a path relative to the root into an absolute path, verifying
// that the result stays inside the root.
//
// Prefer the methods above. This exists for the narrow set of operations that
// os.Root cannot express and that therefore need a real path to hand to another
// API. Every use is a place where the structural guarantee degrades into a
// check, so each one should be deliberate and short-lived.
//
// The comparison is done on cleaned absolute paths with a separator appended,
// never with strings.Contains and never by looking for "..". A substring test
// is wrong in both directions: it rejects the legitimate file "my..notes.md"
// and it accepts an encoded traversal that decodes to dot-dot later. Comparing
// resolved paths asks the only question that matters, which is where the path
// actually landed.
func (r *Root) Resolve(name string) (string, error) {
	cleaned, err := clean(name)
	if err != nil {
		return "", err
	}

	joined := filepath.Join(r.abs, filepath.FromSlash(cleaned))

	// filepath.Join cleans its result, so the check below is comparing two
	// normalized absolute paths. The trailing separator on the prefix stops
	// "/site/posts-backup" from matching a root of "/site/posts".
	prefix := r.abs + string(filepath.Separator)
	if !strings.HasPrefix(joined, prefix) {
		return "", fmt.Errorf("safepath: %q resolves outside the root: %w", cleaned, ErrEscapes)
	}

	// If the path exists, expand symlinks and check again. A symlink can point
	// anywhere, so the lexical check above says nothing about where the file
	// really is. Paths that do not exist yet are allowed through, since callers
	// resolve destinations for files they are about to create, and those are
	// written through the root handle in any case.
	if evaluated, err := filepath.EvalSymlinks(joined); err == nil {
		if !strings.HasPrefix(evaluated, prefix) && evaluated != r.abs {
			return "", fmt.Errorf("safepath: %q is a link leading outside the root: %w", cleaned, ErrEscapes)
		}
		return evaluated, nil
	}

	return joined, nil
}

// ReadDir lists a directory relative to the root.
//
// This exists so a Root satisfies the small directory-listing interface the
// naming helpers take, which lets them be used against either a confined root
// or an in-memory filesystem in tests without either knowing about the other.
func (r *Root) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "." || name == "" {
		return fs.ReadDir(r.FS(), ".")
	}
	cleaned, err := clean(name)
	if err != nil {
		return nil, err
	}
	return fs.ReadDir(r.FS(), cleaned)
}
