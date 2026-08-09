package content

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Posts and uploads share one layout: YYYY/MM/DD/NN-slug.ext.
//
// Two properties make this worth the small amount of ceremony it costs.
//
// First, lexical order equals chronological order. A plain directory listing is
// already in timeline order, which means the index build never sorts by parsing
// a date out of file contents, and a human browsing the folder in a file manager
// sees the same order the site shows.
//
// Second, sharding by date keeps any single directory small. A blog that runs
// for a decade at a post a day puts fewer than a dozen files in any one
// directory, so the pathological case of tens of thousands of entries in a
// single folder, which makes some filesystems and most file managers unhappy,
// simply never arrives.
//
// The NN prefix orders posts within a day and is positional. It is not part of
// the URL and carries no meaning beyond ordering, which is why a slug collision
// is resolved by changing the slug rather than by changing the number.

// sequenceWidth is the zero padding applied to the sequence prefix.
//
// Two digits sorts correctly for the first 99 posts in a day. Beyond that the
// number simply gets wider, which does break strict lexical ordering, since
// "100-" sorts before "99-". That tradeoff is deliberate: the alternative is
// padding every filename to a width nobody will ever need, and a hundred posts
// in one day is a bulk import rather than a person writing. The index sorts on
// the parsed timestamp regardless, so ordering stays correct where it counts.
const sequenceWidth = 2

// postFilePattern matches a stored post filename and captures its sequence
// number and slug.
//
// The sequence is two or more digits so that the overflow case above still
// parses. The slug pattern matches what Slugify produces, which means a file
// renamed by hand into some other shape is not recognized as a post. That is
// intentional. Guessing at a malformed name risks publishing something at an
// unexpected URL, and reporting it lets the author fix it.
var postFilePattern = regexp.MustCompile(`^(\d{2,})-([a-z0-9-]+)\.md$`)

// ErrNotAPostPath reports that a path does not match the storage layout.
var ErrNotAPostPath = errors.New("content: path is not a valid post path")

// PostPath is a parsed post location. It is the bridge between the three forms
// a post identity takes: the file on disk, the URL, and the index entry.
type PostPath struct {
	Year     int
	Month    time.Month
	Day      int
	Sequence int
	Slug     string
}

// BuildPostPath assembles the storage path for a post.
//
// The date comes from the post's published timestamp converted to the site's
// configured location, not to UTC. A post written at 8pm on the 8th in Chicago
// belongs under the 8th, and storing it under the 9th because UTC had already
// rolled over would put it in a folder its author would not think to look in.
func BuildPostPath(published time.Time, sequence int, slug string) string {
	year, month, day := published.Date()
	return fmt.Sprintf("%04d/%02d/%02d/%0*d-%s.md",
		year, month, day, sequenceWidth, sequence, slug)
}

// ParsePostPath extracts the date, sequence, and slug from a stored post path.
//
// The path is expected relative to the posts directory, which is the form
// fs.WalkDir yields during an index build.
func ParsePostPath(p string) (PostPath, error) {
	// Normalize separators so a path that arrived from a Windows filesystem
	// walk parses the same way as one from Linux.
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(path.Clean(p), "./")

	parts := strings.Split(p, "/")
	if len(parts) != 4 {
		return PostPath{}, fmt.Errorf("content: %q has %d path segments, want 4 (YYYY/MM/DD/NN-slug.md): %w",
			p, len(parts), ErrNotAPostPath)
	}

	// Requiring exact widths rather than accepting any integer means "2026/8/8"
	// is rejected rather than silently normalized. Accepting both forms would
	// let the same post exist at two paths, and only one of them would match
	// what BuildPostPath produces on the next save.
	year, err := parseFixedWidthInt(parts[0], 4)
	if err != nil {
		return PostPath{}, fmt.Errorf("content: year segment of %q: %w", p, err)
	}
	month, err := parseFixedWidthInt(parts[1], 2)
	if err != nil {
		return PostPath{}, fmt.Errorf("content: month segment of %q: %w", p, err)
	}
	day, err := parseFixedWidthInt(parts[2], 2)
	if err != nil {
		return PostPath{}, fmt.Errorf("content: day segment of %q: %w", p, err)
	}

	match := postFilePattern.FindStringSubmatch(parts[3])
	if match == nil {
		return PostPath{}, fmt.Errorf("content: filename %q is not NN-slug.md: %w", parts[3], ErrNotAPostPath)
	}
	sequence, err := strconv.Atoi(match[1])
	if err != nil {
		// The pattern guarantees digits, so reaching this means the number
		// overflowed an int, which takes a filename no real tool produces.
		return PostPath{}, fmt.Errorf("content: sequence number in %q: %w", parts[3], ErrNotAPostPath)
	}

	// Verifying the date rejects 2026/02/30 and 2026/13/01, which are
	// well-formed as strings but not real days. Round-tripping through
	// time.Date is the cheapest way to ask, because it normalizes out-of-range
	// values rather than rejecting them, so a mismatch means the input was
	// invalid.
	when := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if when.Year() != year || int(when.Month()) != month || when.Day() != day {
		return PostPath{}, fmt.Errorf("content: %q is not a real date: %w", p, ErrNotAPostPath)
	}

	return PostPath{
		Year:     year,
		Month:    time.Month(month),
		Day:      day,
		Sequence: sequence,
		Slug:     match[2],
	}, nil
}

// parseFixedWidthInt parses a decimal integer that must be exactly width digits.
func parseFixedWidthInt(s string, width int) (int, error) {
	if len(s) != width {
		return 0, fmt.Errorf("%q is %d characters, want exactly %d: %w", s, len(s), width, ErrNotAPostPath)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q contains a non-digit: %w", s, ErrNotAPostPath)
		}
	}
	// strconv cannot fail here, since the loop above proved every character is
	// a digit and the width is small enough that overflow is impossible.
	n, _ := strconv.Atoi(s)
	return n, nil
}

// URL returns the canonical public address of the post.
//
// The sequence prefix is dropped. It orders files within a day and nothing
// more, so exposing it would put a number in the URL that changes meaning if
// posts are ever reordered, and that people would reasonably assume is stable.
func (p PostPath) URL() string {
	return fmt.Sprintf("/%04d/%02d/%02d/%s", p.Year, p.Month, p.Day, p.Slug)
}

// StoragePath returns the path this post occupies relative to the posts
// directory.
func (p PostPath) StoragePath() string {
	return fmt.Sprintf("%04d/%02d/%02d/%0*d-%s.md",
		p.Year, p.Month, p.Day, sequenceWidth, p.Sequence, p.Slug)
}

// DayDir returns the directory holding the post, relative to the posts
// directory.
func (p PostPath) DayDir() string {
	return fmt.Sprintf("%04d/%02d/%02d", p.Year, p.Month, p.Day)
}

// dirLister is the small slice of a filesystem that the allocation helpers
// below need. Taking an interface rather than a concrete root keeps these
// functions testable against fstest.MapFS without touching the disk.
type dirLister interface {
	ReadDir(name string) ([]fs.DirEntry, error)
}

// NextSequence returns the sequence number a new post on the given day should
// take.
//
// The answer is one past the highest number currently present rather than a
// count of the files. Counting breaks as soon as a post is deleted: two files
// numbered 01 and 03 would produce a count of 2, and the new post would collide
// with the existing 02 if it were ever restored, or silently overwrite nothing
// while looking wrong in a listing. Taking the maximum keeps numbers unique for
// the life of the day regardless of what has been removed.
//
// A missing directory means this is the first post of the day, which yields 1.
func NextSequence(fsys dirLister, dayDir string) (int, error) {
	entries, err := fsys.ReadDir(dayDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 1, nil
		}
		return 0, fmt.Errorf("content: reading %q: %w", dayDir, err)
	}

	highest := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := postFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			// Files that do not match the convention are ignored rather than
			// treated as errors. An editor swap file or a stray .DS_Store
			// should not stop someone from publishing.
			continue
		}
		if n, err := strconv.Atoi(match[1]); err == nil && n > highest {
			highest = n
		}
	}

	return highest + 1, nil
}

// AllocateSlug returns a slug that is free within the given day, appending a
// numeric suffix if the preferred slug is already taken.
//
// Collisions are resolved on the slug rather than the sequence number because
// the slug is what appears in the URL and the sequence number is positional.
// Two posts titled "Weekly Notes" on the same day become "weekly-notes" and
// "weekly-notes-2", which reads naturally and stays stable, whereas
// distinguishing them by sequence number would produce two posts competing for
// one URL.
//
// The exclude parameter names a storage path to disregard when checking, which
// is what lets a post keep its own slug while being updated in place. Pass an
// empty string when creating.
func AllocateSlug(fsys dirLister, dayDir, preferred, exclude string) (string, error) {
	taken, err := slugsInDay(fsys, dayDir)
	if err != nil {
		return "", err
	}

	// Drop the post's own slug from the set so that saving an existing post
	// does not see itself as a collision and rename to "-2" on every save.
	if exclude != "" {
		if parsed, err := ParsePostPath(exclude); err == nil {
			delete(taken, parsed.Slug)
		}
	}

	if _, exists := taken[preferred]; !exists {
		return preferred, nil
	}

	// Suffixes start at 2 because the unsuffixed slug is conceptually the
	// first. A "-1" alongside a bare slug would suggest an ordering that does
	// not exist.
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", preferred, n)
		if _, exists := taken[candidate]; !exists {
			return candidate, nil
		}
	}

	// A thousand posts with the same title on one day is not a case worth
	// designing around, but it should fail loudly rather than loop forever.
	return "", fmt.Errorf("content: no free slug for %q in %s after 999 attempts", preferred, dayDir)
}

// slugsInDay returns the set of slugs already used in a day directory.
func slugsInDay(fsys dirLister, dayDir string) (map[string]struct{}, error) {
	taken := make(map[string]struct{})

	entries, err := fsys.ReadDir(dayDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return taken, nil // No directory means no posts, so nothing is taken.
		}
		return nil, fmt.Errorf("content: reading %q: %w", dayDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if match := postFilePattern.FindStringSubmatch(entry.Name()); match != nil {
			taken[match[2]] = struct{}{}
		}
	}
	return taken, nil
}

// NextUploadSequence returns the sequence number a new upload should take
// within a day.
//
// Uploads share the posts' naming scheme but not its file pattern, since an
// upload keeps whatever extension its sniffed type maps to rather than always
// being markdown. This counts anything matching NN-name.ext.
func NextUploadSequence(fsys dirLister, dayDir string) (int, error) {
	entries, err := fsys.ReadDir(dayDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 1, nil
		}
		return 0, fmt.Errorf("content: reading %q: %w", dayDir, err)
	}

	highest := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := uploadFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		// Taking the maximum rather than counting means a deleted file never
		// causes a number to be reused, for the same reason NextSequence does.
		if n, err := strconv.Atoi(match[1]); err == nil && n > highest {
			highest = n
		}
	}
	return highest + 1, nil
}

// uploadFilePattern matches a stored upload filename.
//
// Looser than postFilePattern because the extension varies and because an
// upload slug may be derived from a filename with less predictable content.
var uploadFilePattern = regexp.MustCompile(`^(\d{2,})-(.+)\.[A-Za-z0-9]+$`)
