package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRoot creates a temporary directory, drops a canary file in the parent,
// and returns a Root confined to the child.
//
// The canary matters. Several tests below try to reach outside the root, and a
// test that only asserts "an error occurred" would still pass if the escape
// succeeded but failed for some unrelated reason. Having a real, readable file
// just outside means an escape that works would be visibly successful, so the
// assertions are testing confinement rather than incidental failure.
func newTestRoot(t *testing.T) (*Root, string) {
	t.Helper()

	parent := t.TempDir()
	canary := filepath.Join(parent, "canary.txt")
	if err := os.WriteFile(canary, []byte("this file is outside the root"), 0o644); err != nil {
		t.Fatalf("writing canary file: %v", err)
	}

	inside := filepath.Join(parent, "site")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("creating root directory: %v", err)
	}

	root, err := Open(inside)
	if err != nil {
		t.Fatalf("opening root: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	return root, parent
}

func TestOpenRejectsMissingDirectory(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error opening a root that does not exist, got nil")
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	root, _ := newTestRoot(t)

	want := "# First Light\n\nBody text.\n"
	f, err := root.Create("posts/2026/08/08/01-first-light.md")
	if err != nil {
		t.Fatalf("creating file: %v", err)
	}
	if _, err := f.WriteString(want); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing file: %v", err)
	}

	got, err := root.ReadFile("posts/2026/08/08/01-first-light.md")
	if err != nil {
		t.Fatalf("reading file back: %v", err)
	}
	if string(got) != want {
		t.Errorf("round trip changed the contents:\n got: %q\nwant: %q", got, want)
	}
}

// TestCreateBuildsParentDirectories covers the case the date-sharded layout hits
// constantly, which is the first post of a day landing in a directory tree that
// does not exist yet.
func TestCreateBuildsParentDirectories(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("uploads/2026/12/25/01-photo.jpg")
	if err != nil {
		t.Fatalf("creating a file in a missing directory tree: %v", err)
	}
	f.Close()

	info, err := root.Stat("uploads/2026/12/25")
	if err != nil {
		t.Fatalf("stat on the created directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected the created parent to be a directory")
	}
}

// TestTraversalAttemptsAreRejected walks the standard catalogue of escape
// attempts. None of these should reach the canary file sitting in the parent
// directory.
func TestTraversalAttemptsAreRejected(t *testing.T) {
	root, _ := newTestRoot(t)

	attempts := []struct {
		name string
		path string
	}{
		{"parent directory", "../canary.txt"},
		{"repeated ascent", "../../../../../../etc/passwd"},
		{"bare dot dot", ".."},
		{"ascent after a real directory", "posts/../../canary.txt"},
		{"absolute path", "/etc/passwd"},
		{"absolute path to the canary", "/canary.txt"},
		{"backslash separators", `..\canary.txt`},
		{"mixed separators", `posts\..\..\canary.txt`},
		{"NUL byte truncation", "safe.md\x00../../canary.txt"},
		{"empty path", ""},
		{"root itself", "."},
		{"trailing ascent", "posts/.."},
		{"redundant separators", "posts//..//../canary.txt"},
	}

	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			if _, err := root.ReadFile(attempt.path); err == nil {
				t.Errorf("ReadFile(%q) succeeded, but it should have been refused", attempt.path)
			}

			// Create is the more dangerous direction, since a successful escape
			// here writes attacker-controlled bytes to an arbitrary location
			// rather than merely disclosing a file.
			if f, err := root.Create(attempt.path); err == nil {
				f.Close()
				t.Errorf("Create(%q) succeeded, but it should have been refused", attempt.path)
			}

			if _, err := root.Resolve(attempt.path); err == nil {
				t.Errorf("Resolve(%q) succeeded, but it should have been refused", attempt.path)
			}
		})
	}
}

// TestLiteralDotsInFilenamesAreAllowed is the counterpart to the traversal
// tests, and it is the reason this package never substring-checks for "..".
// These are ordinary filenames that a naive check would reject.
func TestLiteralDotsInFilenamesAreAllowed(t *testing.T) {
	root, _ := newTestRoot(t)

	names := []string{
		"posts/my..notes.md",
		"posts/...md",
		"uploads/version..2.png",
		"posts/2026/08/08/01-a..b.md",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			f, err := root.Create(name)
			if err != nil {
				t.Fatalf("Create(%q) was refused, but it is a legitimate filename: %v", name, err)
			}
			f.Close()

			if !root.Exists(name) {
				t.Errorf("Exists(%q) reported false after the file was created", name)
			}
		})
	}
}

// TestPercentEncodedTraversalIsTreatedAsAFilename confirms that an encoded
// sequence arriving here is just a name. Decoding happens at the HTTP layer, so
// if a request smuggles "%2e%2e" through without being decoded, it must land as
// a literal filename rather than as an ascent.
func TestPercentEncodedTraversalIsTreatedAsAFilename(t *testing.T) {
	root, parent := newTestRoot(t)

	f, err := root.Create("%2e%2e/canary.txt")
	if err != nil {
		t.Fatalf("expected the encoded sequence to be treated as a directory name: %v", err)
	}
	f.Close()

	// The file must have landed inside the root, in a directory literally named
	// "%2e%2e", and the canary in the parent must be untouched.
	if !root.Exists("%2e%2e/canary.txt") {
		t.Error("the file was not created where the literal interpretation says it should be")
	}

	canary, err := os.ReadFile(filepath.Join(parent, "canary.txt"))
	if err != nil {
		t.Fatalf("reading the canary: %v", err)
	}
	if string(canary) != "this file is outside the root" {
		t.Error("the canary file was overwritten, so the encoded path escaped the root")
	}
}

// TestSymlinkEscapeIsRefused covers the case that defeats string inspection
// completely. The path "posts/escape.txt" contains nothing suspicious at all,
// yet it resolves outside the root.
func TestSymlinkEscapeIsRefused(t *testing.T) {
	root, parent := newTestRoot(t)

	if err := root.MkdirAll("posts"); err != nil {
		t.Fatalf("creating the posts directory: %v", err)
	}

	// Plant a symlink inside the root pointing at the canary outside it. This
	// is what a compromised sync client or a careless tarball extraction can
	// leave behind.
	linkPath := filepath.Join(root.Path(), "posts", "escape.txt")
	if err := os.Symlink(filepath.Join(parent, "canary.txt"), linkPath); err != nil {
		t.Skipf("this platform does not allow creating symlinks: %v", err)
	}

	if data, err := root.ReadFile("posts/escape.txt"); err == nil {
		t.Errorf("read through a symlink that leads outside the root and got %q", data)
	}

	if resolved, err := root.Resolve("posts/escape.txt"); err == nil {
		t.Errorf("Resolve followed a symlink out of the root and returned %q", resolved)
	}
}

// TestRelativeSymlinkWithinRootIsAllowed makes sure the symlink handling above
// rejects escapes specifically, rather than rejecting symlinks as a category.
// Someone organizing their content by hand may reasonably link one file to
// another, and that should keep working.
func TestRelativeSymlinkWithinRootIsAllowed(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("posts/real.md")
	if err != nil {
		t.Fatalf("creating the target file: %v", err)
	}
	f.WriteString("real content")
	f.Close()

	// The link target is relative, which is the form that works inside a root.
	// See the test below for why the absolute form does not.
	linkPath := filepath.Join(root.Path(), "posts", "link.md")
	if err := os.Symlink("real.md", linkPath); err != nil {
		t.Skipf("this platform does not allow creating symlinks: %v", err)
	}

	got, err := root.ReadFile("posts/link.md")
	if err != nil {
		t.Fatalf("reading through a relative symlink that stays inside the root: %v", err)
	}
	if string(got) != "real content" {
		t.Errorf("got %q through the symlink, want %q", got, "real content")
	}
}

// TestAbsoluteSymlinkIsRefusedEvenWhenItPointsInside pins down a behavior that
// will otherwise turn up as a confusing bug report.
//
// Inside a root, an absolute symlink target is interpreted relative to the root
// rather than to the real filesystem, so a link pointing at
// /var/site/posts/real.md is read as root + /var/site/posts/real.md and refused.
// That is the correct and safe outcome, since honoring absolute targets would
// hand an attacker a trivial escape. It does mean a hand-built link has to be
// relative, and the error message deserves to say so when this surfaces through
// the API.
func TestAbsoluteSymlinkIsRefusedEvenWhenItPointsInside(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("posts/real.md")
	if err != nil {
		t.Fatalf("creating the target file: %v", err)
	}
	f.WriteString("real content")
	f.Close()

	linkPath := filepath.Join(root.Path(), "posts", "abs-link.md")
	if err := os.Symlink(filepath.Join(root.Path(), "posts", "real.md"), linkPath); err != nil {
		t.Skipf("this platform does not allow creating symlinks: %v", err)
	}

	if _, err := root.ReadFile("posts/abs-link.md"); err == nil {
		t.Error("an absolute symlink was followed, which means absolute targets are being honored inside the root")
	}
}

// TestErrEscapesIsReported checks that rejections are identifiable with
// errors.Is, so callers can distinguish a refused path from a missing file and
// return the right status code.
func TestErrEscapesIsReported(t *testing.T) {
	root, _ := newTestRoot(t)

	_, err := root.ReadFile("../canary.txt")
	if !errors.Is(err, ErrEscapes) {
		t.Errorf("got %v, want an error matching ErrEscapes", err)
	}
}

func TestRenameStaysInsideTheRoot(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("posts/draft.md")
	if err != nil {
		t.Fatalf("creating the source file: %v", err)
	}
	f.WriteString("content")
	f.Close()

	// The legitimate case, which is also the atomic write pattern the store
	// depends on.
	if err := root.Rename("posts/draft.md", "posts/2026/08/08/01-published.md"); err != nil {
		t.Fatalf("renaming within the root: %v", err)
	}
	if !root.Exists("posts/2026/08/08/01-published.md") {
		t.Error("the file is missing from its destination after the rename")
	}

	// The destination must be confined too. A rename that can write outside the
	// root is just as bad as a create that can.
	if err := root.Rename("posts/2026/08/08/01-published.md", "../stolen.md"); err == nil {
		t.Error("renamed a file to a destination outside the root")
	}
}

func TestRemove(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("posts/temporary.md")
	if err != nil {
		t.Fatalf("creating the file: %v", err)
	}
	f.Close()

	if err := root.Remove("posts/temporary.md"); err != nil {
		t.Fatalf("removing the file: %v", err)
	}
	if root.Exists("posts/temporary.md") {
		t.Error("the file still exists after being removed")
	}

	if err := root.Remove("../canary.txt"); err == nil {
		t.Error("removed a file outside the root")
	}
}

// TestFSWalkStaysInsideTheRoot exercises the view the index builder uses. A
// symlinked directory pointing elsewhere must not pull foreign files into the
// walk, because anything the walk yields ends up in the published index.
func TestFSWalkStaysInsideTheRoot(t *testing.T) {
	root, parent := newTestRoot(t)

	for _, name := range []string{"posts/2026/08/08/01-one.md", "posts/2026/08/09/01-two.md"} {
		f, err := root.Create(name)
		if err != nil {
			t.Fatalf("creating %q: %v", name, err)
		}
		f.Close()
	}

	// A directory outside the root holding a file that must never be walked.
	outside := filepath.Join(parent, "secrets")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("creating the outside directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "private.md"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("writing the secret file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root.Path(), "posts", "linked")); err != nil {
		t.Skipf("this platform does not allow creating symlinks: %v", err)
	}

	var found []string
	err := fs.WalkDir(root.FS(), "posts", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A symlinked directory that cannot be traversed surfaces as an
			// error on that entry. Skipping it is the correct response, and it
			// is what the index builder will do.
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the root: %v", err)
	}

	if len(found) != 2 {
		t.Errorf("the walk yielded %d files (%v), want exactly the 2 inside the root", len(found), found)
	}
	for _, p := range found {
		if strings.Contains(p, "linked") {
			t.Errorf("the walk followed a symlink out of the root and yielded %q", p)
		}
	}
}

// TestResolveReturnsUsablePathsForRealFiles confirms the escape hatch works for
// its intended purpose, not just that it rejects bad input.
func TestResolveReturnsUsablePathsForRealFiles(t *testing.T) {
	root, _ := newTestRoot(t)

	f, err := root.Create("posts/real.md")
	if err != nil {
		t.Fatalf("creating the file: %v", err)
	}
	f.Close()

	resolved, err := root.Resolve("posts/real.md")
	if err != nil {
		t.Fatalf("resolving a legitimate path: %v", err)
	}
	if !strings.HasPrefix(resolved, root.Path()) {
		t.Errorf("Resolve returned %q, which is not inside the root %q", resolved, root.Path())
	}
	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("the resolved path is not usable with the os package: %v", err)
	}
}

// TestResolveAllowsPathsThatDoNotExistYet covers destinations for files about to
// be written, which is the main reason Resolve exists.
func TestResolveAllowsPathsThatDoNotExistYet(t *testing.T) {
	root, _ := newTestRoot(t)

	resolved, err := root.Resolve("posts/2027/01/01/01-future.md")
	if err != nil {
		t.Fatalf("resolving a path for a file that does not exist yet: %v", err)
	}
	if !strings.HasPrefix(resolved, root.Path()) {
		t.Errorf("Resolve returned %q, which is not inside the root %q", resolved, root.Path())
	}
}

// TestSiblingDirectoryIsNotMistakenForTheRoot guards the off-by-one that a
// prefix comparison invites. A root at ".../site" must not consider
// ".../site-backup" to be inside it.
func TestSiblingDirectoryIsNotMistakenForTheRoot(t *testing.T) {
	parent := t.TempDir()

	inside := filepath.Join(parent, "site")
	sibling := filepath.Join(parent, "site-backup")
	for _, dir := range []string{inside, sibling} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("creating %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "notes.md"), []byte("sibling"), 0o644); err != nil {
		t.Fatalf("writing the sibling file: %v", err)
	}

	root, err := Open(inside)
	if err != nil {
		t.Fatalf("opening the root: %v", err)
	}
	defer root.Close()

	if _, err := root.Resolve("../site-backup/notes.md"); err == nil {
		t.Error("Resolve accepted a path into a sibling directory whose name shares the root's prefix")
	}
	if _, err := root.ReadFile("../site-backup/notes.md"); err == nil {
		t.Error("ReadFile reached into a sibling directory whose name shares the root's prefix")
	}
}
