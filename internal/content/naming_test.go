package content

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"
)

func TestBuildPostPath(t *testing.T) {
	cases := []struct {
		name      string
		published time.Time
		sequence  int
		slug      string
		want      string
	}{
		{
			name:      "first post of a day",
			published: time.Date(2026, 8, 8, 14, 30, 22, 0, time.UTC),
			sequence:  1,
			slug:      "first-post",
			want:      "2026/08/08/01-first-post.md",
		},
		{
			name:      "single digit month and day are padded",
			published: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
			sequence:  3,
			slug:      "january",
			want:      "2026/01/05/03-january.md",
		},
		{
			name:      "sequence past ninety-nine widens",
			published: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
			sequence:  100,
			slug:      "bulk-import",
			want:      "2026/08/08/100-bulk-import.md",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildPostPath(tc.published, tc.sequence, tc.slug); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildPostPathUsesLocalDate checks that a post lands in the folder for the
// day its author experienced, not the day UTC happened to be on. Writing at 8pm
// in Chicago on the 8th means the 8th, even though UTC has already rolled over.
func TestBuildPostPathUsesLocalDate(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("timezone database is not available: %v", err)
	}

	evening := time.Date(2026, 8, 8, 20, 0, 0, 0, chicago)
	if got, want := BuildPostPath(evening, 1, "evening"), "2026/08/08/01-evening.md"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// The same instant expressed in UTC is already the 9th, which is what the
	// storage layer must not use.
	if got := BuildPostPath(evening.UTC(), 1, "evening"); got == "2026/08/08/01-evening.md" {
		t.Error("expected the UTC conversion to land on the 9th, so this test is no longer proving anything")
	}
}

func TestParsePostPath(t *testing.T) {
	got, err := ParsePostPath("2026/08/08/01-first-post.md")
	if err != nil {
		t.Fatalf("parsing a well-formed path: %v", err)
	}

	want := PostPath{Year: 2026, Month: time.August, Day: 8, Sequence: 1, Slug: "first-post"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParsePostPathRejectsMalformedInput(t *testing.T) {
	bad := []struct {
		path   string
		reason string
	}{
		{"", "empty"},
		{"2026/08/08/01-first-post.txt", "wrong extension"},
		{"2026/08/08/first-post.md", "missing sequence prefix"},
		{"2026/08/08/01-First-Post.md", "uppercase is not a canonical slug"},
		{"2026/08/08/01-first post.md", "space is outside the slug character set"},
		{"2026/8/8/01-first-post.md", "unpadded month and day"},
		{"26/08/08/01-first-post.md", "two-digit year"},
		{"2026/08/01-first-post.md", "too few segments"},
		{"posts/2026/08/08/01-first-post.md", "too many segments"},
		{"2026/13/01/01-post.md", "month thirteen"},
		{"2026/02/30/01-post.md", "February the thirtieth"},
		{"2026/02/29/01-post.md", "February the twenty-ninth in a non-leap year"},
		{"2026/00/08/01-post.md", "month zero"},
		{"2026/08/00/01-post.md", "day zero"},
		{"20a6/08/08/01-post.md", "non-digit in the year"},
		{"2026/08/08/01-.md", "empty slug"},
		{"2026/08/08/-post.md", "no sequence digits"},
	}

	for _, tc := range bad {
		t.Run(tc.reason, func(t *testing.T) {
			if _, err := ParsePostPath(tc.path); err == nil {
				t.Errorf("ParsePostPath(%q) succeeded, want a rejection because %s", tc.path, tc.reason)
			} else if !errors.Is(err, ErrNotAPostPath) {
				t.Errorf("got %v, want an error matching ErrNotAPostPath", err)
			}
		})
	}
}

// TestPostPathRoundTrip is the property the whole naming scheme rests on. A
// path built from components must parse back into exactly those components, or
// the file on disk and the index entry describing it will drift apart.
func TestPostPathRoundTrip(t *testing.T) {
	cases := []PostPath{
		{Year: 2026, Month: time.August, Day: 8, Sequence: 1, Slug: "first-post"},
		{Year: 1999, Month: time.December, Day: 31, Sequence: 99, Slug: "y2k"},
		{Year: 2024, Month: time.February, Day: 29, Sequence: 7, Slug: "leap-day"},
		{Year: 2026, Month: time.January, Day: 1, Sequence: 100, Slug: "a"},
	}

	for _, want := range cases {
		t.Run(want.Slug, func(t *testing.T) {
			got, err := ParsePostPath(want.StoragePath())
			if err != nil {
				t.Fatalf("parsing %q: %v", want.StoragePath(), err)
			}
			if got != want {
				t.Errorf("round trip changed the value:\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

// TestURLDropsTheSequencePrefix documents the deliberate asymmetry between the
// storage path and the public URL.
func TestURLDropsTheSequencePrefix(t *testing.T) {
	p := PostPath{Year: 2026, Month: time.August, Day: 8, Sequence: 3, Slug: "first-post"}

	if got, want := p.URL(), "/2026/08/08/first-post"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
	if got, want := p.StoragePath(), "2026/08/08/03-first-post.md"; got != want {
		t.Errorf("StoragePath() = %q, want %q", got, want)
	}
	if got, want := p.DayDir(), "2026/08/08"; got != want {
		t.Errorf("DayDir() = %q, want %q", got, want)
	}
}

func TestNextSequence(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]*fstest.MapFile
		want  int
	}{
		{
			name:  "empty day starts at one",
			files: map[string]*fstest.MapFile{},
			want:  1,
		},
		{
			name: "follows the highest existing number",
			files: map[string]*fstest.MapFile{
				"2026/08/08/01-one.md": {},
				"2026/08/08/02-two.md": {},
			},
			want: 3,
		},
		{
			// This is the case that rules out counting files. With 02 deleted,
			// a count would return 2 and collide with the existing 02 if it
			// were ever restored from a backup.
			name: "gaps do not cause reuse",
			files: map[string]*fstest.MapFile{
				"2026/08/08/01-one.md":   {},
				"2026/08/08/03-three.md": {},
			},
			want: 4,
		},
		{
			name: "ignores files that do not match the convention",
			files: map[string]*fstest.MapFile{
				"2026/08/08/01-one.md":     {},
				"2026/08/08/.DS_Store":     {},
				"2026/08/08/notes.txt":     {},
				"2026/08/08/.01-one.md.sw": {},
			},
			want: 2,
		},
		{
			name: "handles the widened sequence",
			files: map[string]*fstest.MapFile{
				"2026/08/08/099-a.md": {},
				"2026/08/08/100-b.md": {},
			},
			want: 101,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A MapFS with no matching entries has no such directory, which is
			// exactly the "first post of the day" case.
			fsys := fstest.MapFS(tc.files)
			got, err := NextSequence(fsys, "2026/08/08")
			if err != nil {
				t.Fatalf("NextSequence: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAllocateSlug(t *testing.T) {
	fsys := fstest.MapFS(map[string]*fstest.MapFile{
		"2026/08/08/01-weekly-notes.md":   {},
		"2026/08/08/02-weekly-notes-2.md": {},
		"2026/08/08/03-something-else.md": {},
	})

	cases := []struct {
		name      string
		preferred string
		exclude   string
		want      string
	}{
		{
			name:      "free slug is used as is",
			preferred: "brand-new",
			want:      "brand-new",
		},
		{
			// Suffixes start at 2 because the unsuffixed slug is conceptually
			// the first of its name.
			name:      "first collision skips to a suffix of two",
			preferred: "something-else",
			want:      "something-else-2",
		},
		{
			name:      "finds the next free suffix",
			preferred: "weekly-notes",
			want:      "weekly-notes-3",
		},
		{
			// Without the exclusion, saving an existing post would see its own
			// slug as taken and rename it on every single save, breaking the
			// URL of a published post.
			name:      "a post keeps its own slug when updated",
			preferred: "something-else",
			exclude:   "2026/08/08/03-something-else.md",
			want:      "something-else",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AllocateSlug(fsys, "2026/08/08", tc.preferred, tc.exclude)
			if err != nil {
				t.Fatalf("AllocateSlug: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAllocateSlugOnAnEmptyDay(t *testing.T) {
	got, err := AllocateSlug(fstest.MapFS(nil), "2026/08/08", "first-post", "")
	if err != nil {
		t.Fatalf("AllocateSlug on a day with no directory: %v", err)
	}
	if got != "first-post" {
		t.Errorf("got %q, want the preferred slug used unchanged", got)
	}
}

// TestAllocateSlugCollisionsAreIndependentPerDay confirms that the same title
// published on two different days keeps the clean slug both times. Collision
// handling is scoped to a day because the date is already part of the URL.
func TestAllocateSlugCollisionsAreIndependentPerDay(t *testing.T) {
	fsys := fstest.MapFS(map[string]*fstest.MapFile{
		"2026/08/08/01-weekly-notes.md": {},
	})

	got, err := AllocateSlug(fsys, "2026/08/09", "weekly-notes", "")
	if err != nil {
		t.Fatalf("AllocateSlug: %v", err)
	}
	if got != "weekly-notes" {
		t.Errorf("got %q, want %q because the collision is on a different day", got, "weekly-notes")
	}
}
