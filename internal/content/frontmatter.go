package content

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file implements the frontmatter half of a post file: the YAML block
// between the opening and closing "---" markers.
//
// It is a hand-written parser rather than a call into a YAML library, which
// deserves an explanation since "just use the library" is usually right.
//
// The deciding requirement is that unrecognized keys survive a round trip
// exactly as written. Someone using a third-party client, or keeping their own
// bookkeeping in frontmatter, must not lose that data because Broadside did not
// recognize it. Doing this through a YAML library means decoding into a
// map[string]any and re-encoding, which loses key order, loses comments, and
// rewrites formatting in ways the author did not ask for. Reading a file,
// changing the title, and writing it back would silently reformat unrelated
// lines, and in a product built on hand-editable files that is a genuine defect
// rather than a cosmetic one.
//
// Keeping unknown keys as their original lines sidesteps all of that. The
// parser understands the small set of shapes the known fields use and treats
// everything else as opaque text to be handed back untouched.
//
// The tradeoff is that this is not a general YAML parser and does not try to
// be. Anchors, multi-document streams, and flow mappings are not interpreted.
// For known keys they are not valid input anyway, and for unknown keys they
// pass through byte for byte, which is the behavior that matters.

// fenceMarker opens and closes the frontmatter block.
const fenceMarker = "---"

var (
	// ErrNoFrontmatter reports that a file did not begin with a frontmatter
	// fence. Every post needs one, since the timeline cannot place a file with
	// no published date.
	ErrNoFrontmatter = errors.New("content: file does not begin with a frontmatter fence")

	// ErrUnterminatedFrontmatter reports an opening fence with no closing one,
	// which usually means a truncated write or a hand edit that removed the
	// wrong line.
	ErrUnterminatedFrontmatter = errors.New("content: frontmatter fence is never closed")

	// ErrMissingRequiredField reports that title, slug, or published is absent.
	ErrMissingRequiredField = errors.New("content: required frontmatter field is missing")
)

// ExtraField is a frontmatter key Broadside does not interpret, preserved
// exactly as it appeared in the file.
//
// Lines holds the complete original text of the field, starting with the line
// carrying the key and including any indented continuation lines that belong to
// it. Storing raw lines rather than a parsed value is what makes the round trip
// lossless for nested maps, block sequences, multi-line strings, and anything
// else this parser has no opinion about.
type ExtraField struct {
	Key   string
	Lines []string
}

// Frontmatter is the metadata block at the top of a post file.
type Frontmatter struct {
	Title     string
	Slug      string
	Published time.Time
	Updated   time.Time // Zero when the post has never been edited.
	Draft     bool
	Tags      []string
	Summary   string
	Cover     string

	// Extra holds every key the parser did not recognize, in the order they
	// appeared. See the note on ExtraField for why these are kept as text.
	Extra []ExtraField
}

// Post is a complete post file: its metadata and its markdown body.
type Post struct {
	Frontmatter
	Body string
}

// knownKeys is the set of frontmatter keys the parser interprets. Anything
// outside it is preserved verbatim as an ExtraField.
var knownKeys = map[string]bool{
	"title":     true,
	"slug":      true,
	"published": true,
	"updated":   true,
	"draft":     true,
	"tags":      true,
	"summary":   true,
	"cover":     true,
}

// timeFormats lists the timestamp layouts accepted in published and updated,
// tried in order.
//
// RFC3339 is what Broadside writes and what any API client should send. The
// looser forms exist because the files are meant to be edited by hand, and
// somebody typing a date into a text editor should not have their post
// disappear from the timeline over a missing timezone offset or a space where
// a "T" belongs.
//
// Formats without an offset are interpreted in the site's configured location
// rather than UTC, which is handled by the caller passing that location in.
var timeFormats = []struct {
	layout   string
	hasZone  bool
	dateOnly bool
}{
	{time.RFC3339, true, false},                // 2026-08-08T14:30:22-05:00
	{"2006-01-02T15:04:05", false, false},      // 2026-08-08T14:30:22
	{"2006-01-02 15:04:05Z07:00", true, false}, // 2026-08-08 14:30:22-05:00
	{"2006-01-02 15:04:05", false, false},      // 2026-08-08 14:30:22
	{"2006-01-02 15:04", false, false},         // 2026-08-08 14:30
	{"2006-01-02", false, true},                // 2026-08-08
}

// ParseTime reads a timestamp in any of the accepted layouts.
//
// A value with no timezone offset is interpreted in loc, so a hand-written
// "2026-08-08 14:30" means half past two in the afternoon where the author
// lives rather than in UTC. A date with no time lands at midnight, which puts
// the post at the start of its day in the timeline.
func ParseTime(value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("content: empty timestamp")
	}
	if loc == nil {
		loc = time.Local
	}

	for _, format := range timeFormats {
		if format.hasZone {
			if t, err := time.Parse(format.layout, value); err == nil {
				return t, nil
			}
			continue
		}
		// ParseInLocation is what attaches the site's location to a value that
		// carries no offset of its own. time.Parse would silently assume UTC
		// and shift the post by however many hours the author is from
		// Greenwich.
		if t, err := time.ParseInLocation(format.layout, value, loc); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("content: %q is not a recognized timestamp", value)
}

// Split separates a post file into its raw frontmatter block and its body.
//
// The returned frontmatter excludes both fences. The body keeps its internal
// formatting exactly, with only the single newline that follows the closing
// fence removed, so that markdown indentation and blank lines survive
// unchanged.
func Split(source string) (frontmatter, body string, err error) {
	// A byte order mark is invisible in an editor but stops the fence from
	// matching, which produces a baffling error on a file that visibly has
	// frontmatter. Editors on Windows add one often enough to be worth
	// handling. It is written as an escape because a literal BOM in Go source
	// is itself a compile error.
	source = strings.TrimPrefix(source, "\ufeff")

	// Normalizing line endings means a file written on Windows parses
	// identically to one written on Linux. The body is normalized too, since
	// markdown rendering does not care and mixed endings in stored files cause
	// confusing diffs later.
	source = strings.ReplaceAll(source, "\r\n", "\n")

	rest, ok := strings.CutPrefix(source, fenceMarker+"\n")
	if !ok {
		// A file consisting of nothing but the fence line is malformed rather
		// than missing frontmatter, but the distinction does not help anyone,
		// so both report the same thing.
		return "", "", ErrNoFrontmatter
	}

	// The closing fence must be a line of its own. Searching for "\n---\n"
	// rather than just "---" avoids matching a horizontal rule or a YAML
	// document separator that happens to sit inside a value.
	end := strings.Index(rest, "\n"+fenceMarker)
	if end < 0 {
		return "", "", ErrUnterminatedFrontmatter
	}

	frontmatter = rest[:end]

	// Step past the closing fence and whatever follows it on that line, which
	// should be nothing but may include trailing whitespace.
	afterFence := rest[end+1+len(fenceMarker):]
	if newline := strings.IndexByte(afterFence, '\n'); newline >= 0 {
		body = afterFence[newline+1:]
	} else {
		// The file ends at the closing fence, so the post has no body. That is
		// unusual but not an error; a post can be a title and a date.
		return frontmatter, "", nil
	}

	// Consume the single blank line that conventionally separates the closing
	// fence from the body. Marshal always writes one, so without dropping
	// exactly one here the two functions would not be inverses, and every save
	// would prepend another newline to the body until the file slowly filled
	// with blank lines.
	//
	// TrimPrefix removes one occurrence, which is what makes this symmetric
	// rather than destructive. A body that genuinely starts with two blank
	// lines keeps one of them.
	body = strings.TrimPrefix(body, "\n")

	return frontmatter, body, nil
}

// ParsePost reads a complete post file.
//
// Missing required fields are not treated as a parse failure. A file that is
// structurally sound but incomplete still parses, so that the store can fill
// the gaps from the file's own path before deciding anything is wrong. Call
// Validate when the fields genuinely have to be present.
func ParsePost(source string, loc *time.Location) (Post, error) {
	raw, body, err := Split(source)
	if err != nil {
		return Post{}, err
	}

	fm, err := ParseFrontmatter(raw, loc)
	if err != nil {
		return Post{}, err
	}

	return Post{Frontmatter: fm, Body: body}, nil
}

// ParseFrontmatter reads the metadata block, which must not include the fences.
func ParseFrontmatter(raw string, loc *time.Location) (Frontmatter, error) {
	var fm Frontmatter

	lines := strings.Split(raw, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Blank lines and comments carry no field. They are dropped rather than
		// preserved, which is the one place this parser is not byte exact.
		// Attaching a comment to the right field reliably is more machinery
		// than the feature is worth, and a comment in frontmatter is rare next
		// to an unrecognized key, which is preserved properly.
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		// A line that is indented or starts a list item belongs to the field
		// above it. Reaching one here means the block began with a
		// continuation, which is malformed, so it is skipped rather than
		// treated as a key.
		if isContinuation(line) {
			continue
		}

		key, value, ok := splitKeyValue(line)
		if !ok {
			// Not a key line and not a continuation. Rather than fail the whole
			// post over one bad line, keep it as an anonymous extra so it
			// survives the round trip and stays visible to the author.
			fm.Extra = append(fm.Extra, ExtraField{Key: "", Lines: []string{line}})
			continue
		}

		// Gather any continuation lines that belong to this key, which is what
		// makes block sequences and nested maps hang together as one field.
		block := []string{line}
		for i+1 < len(lines) && isContinuation(lines[i+1]) {
			i++
			block = append(block, lines[i])
		}

		normalized := strings.ToLower(key)
		if !knownKeys[normalized] {
			fm.Extra = append(fm.Extra, ExtraField{Key: key, Lines: block})
			continue
		}

		if err := fm.assign(normalized, value, block, loc); err != nil {
			return Frontmatter{}, err
		}
	}

	return fm, nil
}

// assign stores a parsed value into the matching known field.
func (fm *Frontmatter) assign(key, value string, block []string, loc *time.Location) error {
	switch key {
	case "title":
		fm.Title = unquote(value)
	case "slug":
		fm.Slug = unquote(value)
	case "summary":
		fm.Summary = unquote(value)
	case "cover":
		fm.Cover = unquote(value)

	case "draft":
		fm.Draft = parseBool(value)

	case "published", "updated":
		// An empty value is treated as absent rather than as an error, since
		// "updated:" with nothing after it is a common result of an edit that
		// removed the value but not the key.
		if strings.TrimSpace(value) == "" {
			return nil
		}
		t, err := ParseTime(unquote(value), loc)
		if err != nil {
			return fmt.Errorf("content: %s field: %w", key, err)
		}
		if key == "published" {
			fm.Published = t
		} else {
			fm.Updated = t
		}

	case "tags":
		fm.Tags = parseTags(value, block)
	}

	return nil
}

// isContinuation reports whether a line belongs to the field declared above it.
//
// Indentation is the YAML signal for nesting, and a leading dash marks a block
// sequence item, which by convention may sit at the same indentation as its
// key.
func isContinuation(line string) bool {
	if line == "" {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' {
		return true
	}
	return strings.HasPrefix(line, "- ") || line == "-"
}

// splitKeyValue separates "key: value" into its parts.
//
// The separator is a colon followed by whitespace or end of line, not merely a
// colon. Requiring the space is what keeps a value such as a timestamp or a URL
// from being cut at its first colon.
func splitKeyValue(line string) (key, value string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != ':' {
			continue
		}
		// A colon at end of line opens a block value, so the key is everything
		// before it and the value is empty.
		if i == len(line)-1 {
			return strings.TrimSpace(line[:i]), "", isPlausibleKey(line[:i])
		}
		if line[i+1] == ' ' || line[i+1] == '\t' {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), isPlausibleKey(line[:i])
		}
	}
	return "", "", false
}

// isPlausibleKey rejects text that happens to contain a colon but is not a
// field name, so that a stray prose line does not become a key.
func isPlausibleKey(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// unquote removes surrounding quotes from a scalar value and interprets the
// escape sequences YAML defines inside double quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}

	first, last := s[0], s[len(s)-1]

	// Single quotes are literal in YAML apart from a doubled quote meaning one
	// quote, so no escape processing applies.
	if first == '\'' && last == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}

	if first == '"' && last == '"' {
		// strconv.Unquote handles the escape sequences correctly for the cases
		// that actually occur. It is strict about things YAML permits and Go
		// does not, so a failure falls back to stripping the quotes, which is
		// closer to the author's intent than returning the raw text.
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
		return s[1 : len(s)-1]
	}

	return s
}

// parseBool interprets the spellings YAML accepts for a boolean.
//
// Anything unrecognized is false. For the draft flag specifically that is the
// safe direction to guess wrong in: a typo that leaves a post published is
// visible immediately, whereas one that hides it might not be noticed for
// weeks.
func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(unquote(value))) {
	case "true", "yes", "on", "y", "1":
		return true
	default:
		return false
	}
}

// parseTags reads a tag list in either of the two shapes YAML offers.
//
// The flow form sits on the key's own line:
//
//	tags: [astro, sv503]
//
// The block form uses indented list items:
//
//	tags:
//	  - astro
//	  - sv503
//
// Both are common in hand-written files, so both are accepted. Broadside always
// writes the flow form, since it keeps a post's metadata compact.
func parseTags(value string, block []string) []string {
	var raw []string

	if trimmed := strings.TrimSpace(value); trimmed != "" {
		// Flow form. The brackets are optional in practice, since a bare
		// comma-separated list is what people tend to type.
		trimmed = strings.TrimPrefix(trimmed, "[")
		trimmed = strings.TrimSuffix(trimmed, "]")
		raw = strings.Split(trimmed, ",")
	} else {
		// Block form. The first line is the key itself, so it is skipped.
		for _, line := range block[1:] {
			item := strings.TrimSpace(line)
			if item, ok := strings.CutPrefix(item, "-"); ok {
				raw = append(raw, item)
			}
		}
	}

	// Tags become URL segments on tag pages, so they go through the same
	// slugifier as titles. Doing it at parse time means the index, the tag
	// pages, and the links between them all agree without any of them having to
	// remember to normalize.
	seen := make(map[string]struct{}, len(raw))
	tags := make([]string, 0, len(raw))
	for _, item := range raw {
		tag := Slugify(unquote(item))
		if tag == "" {
			continue
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}

	if len(tags) == 0 {
		return nil
	}
	return tags
}
