package content

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"git.thebytes.net/roberts/broadside/internal/safepath"
)

// Directory names inside the site root. These are fixed rather than
// configurable, because a layout people can rearrange is a layout that cannot
// be documented in one diagram, and "move the folder and it works" depends on
// every Broadside site looking the same.
const (
	PostsDir     = "posts"
	UploadsDir   = "uploads"
	RevisionsDir = "core/revisions"
)

// maxRevisionsPerPost caps how much history is kept for a single post.
//
// Twenty is enough to recover from the realistic failure, which is noticing a
// bad edit within a few saves, without letting the revisions folder grow
// without bound on a post someone edits all afternoon.
const maxRevisionsPerPost = 20

// ErrNotFound reports that no post exists at the requested location.
var ErrNotFound = errors.New("content: post not found")

// Store reads and writes post files.
//
// All writes are serialized through a single mutex. That is a deliberate
// choice to keep the flat-file model simple rather than a performance
// oversight: a single-author blog does not have concurrent writers worth
// optimizing for, and the alternative is per-file locking that has to stay
// correct across renames, revisions, and index updates. Reads are not blocked
// by the mutex, since each one opens its own file handle.
type Store struct {
	root *safepath.Root
	loc  *time.Location

	// writeMu serializes every mutation. It covers the whole save sequence,
	// including allocating a sequence number, so that two posts created in the
	// same instant cannot be handed the same one.
	writeMu sync.Mutex
}

// NewStore creates a store rooted at the site directory.
//
// The location is used to decide which day a post is filed under and to
// interpret timestamps written without an offset.
func NewStore(root *safepath.Root, loc *time.Location) *Store {
	if loc == nil {
		loc = time.UTC
	}
	return &Store{root: root, loc: loc}
}

// Root exposes the underlying confined filesystem, for callers that need to
// walk the tree or read files the store has no specific method for.
func (s *Store) Root() *safepath.Root { return s.root }

// Location returns the store's configured timezone.
func (s *Store) Location() *time.Location { return s.loc }

// postsFS returns a filesystem view rooted at the posts directory, which is the
// form the naming helpers and the index walk both expect.
func (s *Store) postsFS() fs.FS {
	sub, err := fs.Sub(s.root.FS(), PostsDir)
	if err != nil {
		// fs.Sub only fails on an invalid path, and PostsDir is a constant, so
		// this cannot happen at runtime. Returning an empty filesystem keeps
		// callers from having to handle an impossible error.
		return fs.FS(emptyFS{})
	}
	return sub
}

// emptyFS stands in for a posts directory that could not be opened, so that
// callers see "no posts" rather than a panic.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// Read loads a single post by its storage path, which is relative to the posts
// directory.
//
// The post is reconciled against its own path before being returned, so a file
// written by hand with an incomplete header still produces a usable value.
func (s *Store) Read(storagePath string) (Post, error) {
	parsed, err := ParsePostPath(storagePath)
	if err != nil {
		return Post{}, err
	}

	data, err := s.root.ReadFile(path.Join(PostsDir, storagePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Post{}, fmt.Errorf("content: %s: %w", storagePath, ErrNotFound)
		}
		return Post{}, fmt.Errorf("content: reading %s: %w", storagePath, err)
	}

	post, err := ParsePost(string(data), s.loc)
	if err != nil {
		return Post{}, fmt.Errorf("content: parsing %s: %w", storagePath, err)
	}

	post.Reconcile(parsed, s.loc)
	return post, nil
}

// Create writes a new post and returns the path it was stored at.
//
// The sequence number and any slug collision are resolved here, under the write
// lock, because both depend on what else exists in the target day and both
// would race if resolved by the caller.
func (s *Store) Create(post Post) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if post.Published.IsZero() {
		post.Published = time.Now().In(s.loc)
	} else {
		// Converting into the site's location is what decides which day folder
		// the post lands in. A timestamp arriving from an API client in UTC
		// must be filed under the author's day, not Greenwich's.
		post.Published = post.Published.In(s.loc)
	}

	// A title that produces no slug, such as one written entirely in a
	// non-Latin script, falls back to the date so the filename stays valid.
	preferred := post.Slug
	if preferred == "" {
		preferred = SlugifyWithFallback(post.Title, post.Published.Format("2006-01-02"))
	} else {
		preferred = SlugifyWithFallback(preferred, post.Published.Format("2006-01-02"))
	}

	year, month, day := post.Published.Date()
	dayDir := fmt.Sprintf("%04d/%02d/%02d", year, month, day)

	fsys := s.postsFS()

	slug, err := AllocateSlug(readDirFS{fsys}, dayDir, preferred, "")
	if err != nil {
		return "", err
	}
	sequence, err := NextSequence(readDirFS{fsys}, dayDir)
	if err != nil {
		return "", err
	}

	post.Slug = slug
	storagePath := BuildPostPath(post.Published, sequence, slug)

	if err := s.writeFile(path.Join(PostsDir, storagePath), post.Marshal()); err != nil {
		return "", err
	}
	return storagePath, nil
}

// Update rewrites an existing post in place.
//
// The storage path does not change even if the title does, which is the
// behavior that keeps published URLs stable. Changing the slug is possible, but
// it is an explicit act by the caller rather than a side effect of renaming a
// post.
func (s *Store) Update(storagePath string, post Post) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	parsed, err := ParsePostPath(storagePath)
	if err != nil {
		return err
	}

	full := path.Join(PostsDir, storagePath)
	if !s.root.Exists(full) {
		return fmt.Errorf("content: %s: %w", storagePath, ErrNotFound)
	}

	// Stamp the edit time. This is done here rather than left to the caller so
	// that every path through the API gets it right, including Micropub
	// clients that know nothing about the field.
	post.Updated = time.Now().In(s.loc)

	if post.Slug == "" {
		post.Slug = parsed.Slug
	}

	// Snapshot the previous contents before overwriting. Doing this inside the
	// write lock means a revision can never be skipped by a concurrent save.
	if err := s.saveRevision(storagePath); err != nil {
		return err
	}

	return s.writeFile(full, post.Marshal())
}

// Delete removes a post, keeping its revision history.
//
// The revisions are deliberately left behind. Deleting is the one action with
// no undo, and the history is the only thing that makes it recoverable.
func (s *Store) Delete(storagePath string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := ParsePostPath(storagePath); err != nil {
		return err
	}

	// Take a final revision so the deleted content is recoverable even if no
	// edit was ever made.
	if err := s.saveRevision(storagePath); err != nil {
		return err
	}

	if err := s.root.Remove(path.Join(PostsDir, storagePath)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("content: %s: %w", storagePath, ErrNotFound)
		}
		return fmt.Errorf("content: deleting %s: %w", storagePath, err)
	}
	return nil
}

// writeFile writes data atomically.
//
// The sequence is write to a temporary file in the same directory, flush it to
// disk, then rename over the target. On POSIX a rename within a filesystem is
// atomic, so a reader either sees the old file or the new one and never a
// half-written mixture. The temporary file has to be in the same directory
// because a rename across filesystems is not atomic and may not be permitted
// at all.
//
// The fsync before the rename is what makes this survive a power loss rather
// than merely a crash. Without it the rename can reach disk before the
// contents do, which leaves a file that exists but is empty.
func (s *Store) writeFile(fullPath, data string) error {
	dir := path.Dir(fullPath)
	if err := s.root.MkdirAll(dir); err != nil {
		return fmt.Errorf("content: creating %s: %w", dir, err)
	}

	// The temporary name includes the process id and a nanosecond timestamp so
	// that two Broadside instances pointed at the same folder, which is not
	// supported but does happen, cannot collide on it.
	tempPath := fmt.Sprintf("%s.tmp-%d-%d", fullPath, os.Getpid(), time.Now().UnixNano())

	f, err := s.root.Create(tempPath)
	if err != nil {
		return fmt.Errorf("content: creating temporary file: %w", err)
	}

	// Any failure from here on has to clean up the temporary file, otherwise a
	// failing disk slowly litters the content folder with debris.
	cleanup := func() { s.root.Remove(tempPath) }

	if _, err := f.WriteString(data); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("content: writing temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("content: flushing temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("content: closing temporary file: %w", err)
	}

	if err := s.root.Rename(tempPath, fullPath); err != nil {
		cleanup()
		return fmt.Errorf("content: replacing %s: %w", fullPath, err)
	}
	return nil
}

// saveRevision copies the current contents of a post into the revisions folder.
//
// A missing post is not an error, since Create has no previous version to
// snapshot and Delete may be called on something already gone.
func (s *Store) saveRevision(storagePath string) error {
	data, err := s.root.ReadFile(path.Join(PostsDir, storagePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("content: reading %s for revision: %w", storagePath, err)
	}

	dir := revisionDir(storagePath)

	// The filename is a compact timestamp, which sorts chronologically as text
	// and contains no characters that need escaping on any filesystem. A colon
	// would be illegal on Windows, so RFC3339 itself is not usable here.
	name := time.Now().In(s.loc).Format("20060102T150405")
	if err := s.writeFile(path.Join(dir, name+".md"), string(data)); err != nil {
		return err
	}

	return s.pruneRevisions(dir)
}

// revisionDir returns the folder holding a post's history.
//
// The post's own storage path is reused, minus the extension, which keeps the
// revisions tree mirroring the posts tree and makes it obvious by eye which
// history belongs to which post.
func revisionDir(storagePath string) string {
	return path.Join(RevisionsDir, strings.TrimSuffix(storagePath, ".md"))
}

// pruneRevisions deletes the oldest snapshots beyond the retention limit.
func (s *Store) pruneRevisions(dir string) error {
	entries, err := s.readDir(dir)
	if err != nil {
		return nil // Nothing to prune if the directory cannot be listed.
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= maxRevisionsPerPost {
		return nil
	}

	// The timestamp format sorts chronologically as plain text, so a lexical
	// sort puts the oldest first without parsing anything.
	sort.Strings(names)

	for _, name := range names[:len(names)-maxRevisionsPerPost] {
		if err := s.root.Remove(path.Join(dir, name)); err != nil {
			return fmt.Errorf("content: pruning revision %s: %w", name, err)
		}
	}
	return nil
}

// Revisions lists the saved snapshots of a post, newest first.
func (s *Store) Revisions(storagePath string) ([]string, error) {
	// The path is validated before it is used to build a directory name, for
	// the same reason handleUpload confines uploads to their own root: joining
	// an unvalidated segment onto a prefix lets ".." resolve against that
	// prefix and land somewhere legitimate-looking but wrong. Here
	// "../config.json" would otherwise join to "core/config.json", which is a
	// real file inside the root and would be read without complaint.
	if _, err := ParsePostPath(storagePath); err != nil {
		return nil, err
	}

	entries, err := s.readDir(revisionDir(storagePath))
	if err != nil {
		return nil, nil // A post with no history is not an error.
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// ReadRevision loads one historical version of a post.
func (s *Store) ReadRevision(storagePath, revision string) (Post, error) {
	// Both arguments are attacker-controlled once this is reachable from a
	// route, so both are validated. See Revisions for what an unvalidated
	// storage path would allow.
	if _, err := ParsePostPath(storagePath); err != nil {
		return Post{}, err
	}

	// The revision name comes from a URL, so it is slugified before being used
	// as a path component. The timestamp format contains only digits and a "T",
	// all of which survive, so a name that changes here was not a real
	// revision to begin with.
	safe := Slugify(revision)
	if safe == "" {
		return Post{}, fmt.Errorf("content: %q is not a valid revision name: %w", revision, ErrNotFound)
	}

	data, err := s.root.ReadFile(path.Join(revisionDir(storagePath), safe+".md"))
	if err != nil {
		return Post{}, fmt.Errorf("content: revision %s of %s: %w", revision, storagePath, ErrNotFound)
	}

	return ParsePost(string(data), s.loc)
}

// readDir lists a directory relative to the site root.
func (s *Store) readDir(dir string) ([]fs.DirEntry, error) {
	return fs.ReadDir(s.root.FS(), dir)
}

// readDirFS adapts an fs.FS to the small interface the naming helpers take.
// Keeping that interface narrow is what lets them be tested against an
// in-memory filesystem with no disk involved.
type readDirFS struct{ fsys fs.FS }

func (r readDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(r.fsys, name)
}

// Walk visits every post file in storage order, which is chronological because
// of how the paths are laid out.
//
// The callback receives the storage path relative to the posts directory. Files
// that do not match the naming convention are skipped rather than reported,
// since a stray editor swap file or a .DS_Store should not interrupt an index
// build.
func (s *Store) Walk(visit func(storagePath string) error) error {
	fsys := s.postsFS()

	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory, which includes a symlink that leads out
			// of the root, is skipped rather than failing the whole walk. One
			// bad entry should not take down the index.
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		if _, err := ParsePostPath(p); err != nil {
			return nil
		}
		return visit(p)
	})
}
