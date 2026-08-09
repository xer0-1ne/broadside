package safepath

import (
	"os"
	"path"
	"path/filepath"
	"testing"
)

// TestSubRootConfinesToTheSubdirectory covers a traversal that was live in the
// upload handler and is worth pinning permanently, because the code that had
// the bug looked careful.
//
// The handler took the request path, joined it onto "uploads/", and read the
// result through a Root confined to the site directory. That reads as a
// reasonable thing to do, and it was wrong: path.Join resolves ".." against the
// prefix, so "uploads" plus "../core/config.json" collapses to
// "core/config.json". By the time the Root saw it, there was no traversal left
// to catch, only a clean path to a real file inside the root it was guarding.
// It returned the config file, exactly as it was designed to.
//
// The lesson is that a Root confines to one directory, so the boundary has to
// be the directory that actually matters. These tests hold that line.
func TestSubRootConfinesToTheSubdirectory(t *testing.T) {
	siteRoot := t.TempDir()

	// A site layout with a secret in core and an image in uploads.
	for _, dir := range []string{"core", "uploads/2026/08/08"} {
		if err := os.MkdirAll(filepath.Join(siteRoot, dir), 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}

	secret := []byte(`{"password_hash":"do not serve this"}`)
	if err := os.WriteFile(filepath.Join(siteRoot, "core", "auth.json"), secret, 0o600); err != nil {
		t.Fatalf("writing the secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "uploads", "2026", "08", "08", "01-photo.png"), []byte("image bytes"), 0o644); err != nil {
		t.Fatalf("writing the image: %v", err)
	}

	site, err := Open(siteRoot)
	if err != nil {
		t.Fatalf("opening the site root: %v", err)
	}
	defer site.Close()

	uploads, err := site.Sub("uploads")
	if err != nil {
		t.Fatalf("opening the uploads sub-root: %v", err)
	}
	defer uploads.Close()

	// The exact request that leaked. PathValue decodes percent-encoding, so
	// "%2e%2e" reaches the handler as "..", which is what is passed here.
	attacks := []string{
		"../core/auth.json",
		"../../etc/passwd",
		"2026/../../core/auth.json",
		"./../core/auth.json",
		"../core/../core/auth.json",
	}

	for _, attack := range attacks {
		t.Run(attack, func(t *testing.T) {
			if data, err := uploads.ReadFile(attack); err == nil {
				t.Errorf("read %q through the uploads root and got %q", attack, data)
			}
		})
	}

	// The legitimate request still works, so the fix confines rather than
	// simply breaking the handler.
	got, err := uploads.ReadFile("2026/08/08/01-photo.png")
	if err != nil {
		t.Fatalf("reading a real upload: %v", err)
	}
	if string(got) != "image bytes" {
		t.Errorf("got %q, want the image contents", got)
	}
}

// TestJoinBeforeConfiningIsUnsafe demonstrates the mechanism directly, so that
// anyone reading these tests sees why the sub-root exists rather than having to
// reconstruct it from the fix.
func TestJoinBeforeConfiningIsUnsafe(t *testing.T) {
	// This is what the buggy handler computed. The assertion is about
	// path.Join's behavior rather than about this package, and it is the whole
	// reason the vulnerability existed.
	joined := path.Join("uploads", "../core/config.json")

	if joined != "core/config.json" {
		t.Fatalf("path.Join produced %q; this test no longer demonstrates the hazard", joined)
	}

	// The result contains no traversal at all, so any check looking for ".."
	// at this point sees a perfectly ordinary path.
	if _, err := clean(joined); err != nil {
		t.Errorf("the joined path was rejected by clean, but the point is that it is accepted: %v", err)
	}
}
