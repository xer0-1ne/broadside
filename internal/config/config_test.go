package config

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"git.thebytes.net/roberts/broadside/internal/safepath"
)

// newTestRoot makes a throwaway site folder confined the same way the real one
// is, so these exercise the actual write path rather than a plain os.WriteFile.
func newTestRoot(t *testing.T) *safepath.Root {
	t.Helper()

	root, err := safepath.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a test root: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	if err := root.MkdirAll("core"); err != nil {
		t.Fatalf("creating core: %v", err)
	}
	return root
}

// TestConcurrentSavesNeverCorruptTheFile covers the failure that adding the
// settings API made reachable.
//
// Before, the only thing that wrote config.json was the admin form, driven by
// one browser session at a time, so writing the file in place was very unlikely
// to interleave. The API is a second writer, and a phone saving settings while
// an automation does the same is now an ordinary thing to happen. The old code
// truncated the file and then filled it, so a second writer arriving in that
// gap did not lose an update, it produced a document that no longer parsed and
// took every setting on the site with it.
func TestConcurrentSavesNeverCorruptTheFile(t *testing.T) {
	root := newTestRoot(t)

	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cfg := Default()
			cfg.Title = fmt.Sprintf("Title %02d", n)
			cfg.PostsPerPage = 10 + n
			if err := Save(root, cfg); err != nil {
				t.Errorf("Save: %v", err)
			}
		}(i)
	}

	// Readers run at the same time, since a torn file is only a problem
	// because something is trying to read it.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Load(root); err != nil {
				t.Errorf("Load during concurrent saves: %v", err)
			}
		}()
	}

	wg.Wait()

	// Whatever landed last has to be a complete, parseable config rather than
	// a mixture of two of them.
	final, err := Load(root)
	if err != nil {
		t.Fatalf("the file did not survive: %v", err)
	}
	if !strings.HasPrefix(final.Title, "Title ") {
		t.Errorf("the final title looks torn: %q", final.Title)
	}
	if final.PostsPerPage < 10 || final.PostsPerPage > 49 {
		t.Errorf("the final page size looks torn: %d", final.PostsPerPage)
	}
}

// TestSaveLeavesNoDebris checks the temporary files are cleaned up, since a
// site folder slowly filling with config.json.tmp-* would be its own problem.
func TestSaveLeavesNoDebris(t *testing.T) {
	root := newTestRoot(t)

	for range 5 {
		if err := Save(root, Default()); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	entries, err := root.ReadDir("core")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}
