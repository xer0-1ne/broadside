package content

import (
	"fmt"
	"strings"
	"time"
)

// This file turns a Post back into the text of a file. It is the other half of
// the round trip that frontmatter.go begins, and the two have to agree exactly:
// anything written here must parse back into the value that produced it.

// Marshal renders a post as the complete contents of its file.
//
// Known fields are written first, in a fixed order, followed by any
// unrecognized fields exactly as they arrived. The fixed order matters more
// than it might seem. Without it, a post's file would shuffle its own lines
// between saves depending on map iteration order, and every save would produce
// a large diff in a folder people are encouraged to keep in git.
func (p Post) Marshal() string {
	var b strings.Builder

	b.WriteString(fenceMarker)
	b.WriteByte('\n')

	// Required fields lead, because they are what someone opening the file
	// wants to see first.
	writeScalar(&b, "title", p.Title)
	writeScalar(&b, "slug", p.Slug)
	writeScalar(&b, "published", formatTime(p.Published))

	// Optional fields are omitted entirely when empty rather than written as
	// blanks. A file full of empty keys is noise, and the parser treats an
	// absent key and an empty one identically anyway.
	if !p.Updated.IsZero() {
		writeScalar(&b, "updated", formatTime(p.Updated))
	}
	if p.Draft {
		// Only written when true. A post with "draft: false" on every line adds
		// nothing, and its absence already means published.
		b.WriteString("draft: true\n")
	}
	if len(p.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(p.Tags, ", "))
	}
	if p.Summary != "" {
		writeScalar(&b, "summary", p.Summary)
	}
	if p.Cover != "" {
		writeScalar(&b, "cover", p.Cover)
	}

	// Unrecognized fields are replayed verbatim. This is the whole point of
	// keeping them as lines: whatever the author or a third-party client wrote
	// comes back byte for byte, including its own indentation and structure.
	for _, extra := range p.Extra {
		for _, line := range extra.Lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	b.WriteString(fenceMarker)
	b.WriteByte('\n')

	if p.Body != "" {
		// A blank line after the closing fence keeps the file readable and
		// matches what every markdown editor produces. Split tolerates its
		// absence, so this is presentation rather than structure.
		b.WriteByte('\n')
		b.WriteString(p.Body)

		// Files end with a newline. Editors, diff tools, and POSIX all expect
		// it, and its absence shows up as a spurious change in git.
		if !strings.HasSuffix(p.Body, "\n") {
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// formatTime renders a timestamp in the one layout Broadside writes.
//
// RFC3339 with an explicit offset is used even though the parser accepts looser
// forms, because a stored timestamp with no offset is ambiguous the moment the
// site's configured timezone changes or the folder moves to another machine.
// Preserving the offset means the instant a post was published stays fixed
// regardless of where the files end up.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// writeScalar writes one "key: value" line, quoting the value if it needs it.
func writeScalar(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(quoteIfNeeded(value))
	b.WriteByte('\n')
}

// quoteIfNeeded wraps a scalar in double quotes when leaving it bare would
// change how it parses.
//
// Most titles need no quoting, and quoting everything would make the files
// noticeably uglier for no benefit in a product whose premise is that people
// read and edit these files directly. So the rule is to quote only what has to
// be quoted, which is anything that would otherwise be read as structure rather
// than as text.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}

	// Leading or trailing whitespace would be trimmed on the way back in, so it
	// has to be protected to survive.
	if strings.TrimSpace(s) != s {
		return quote(s)
	}

	// A colon followed by a space is the key separator, so a title containing
	// one would be split at the wrong place. This is the common case, since it
	// covers every title of the form "Something: A Subtitle".
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return quote(s)
	}

	// A "#" preceded by whitespace starts a YAML comment, which would truncate
	// the value at that point.
	if strings.Contains(s, " #") {
		return quote(s)
	}

	// Control characters, including any embedded newline, cannot appear in a
	// bare scalar without breaking the line structure of the file.
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return quote(s)
		}
	}

	// A leading character that YAML reads as syntax has to be quoted, otherwise
	// the value becomes a list item, an anchor, a flow collection, and so on.
	switch s[0] {
	case '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
		return quote(s)
	}

	// A bare value that YAML would read as a non-string type has to be quoted
	// to stay a string. Without this, a post titled "true" or "2026-08-08"
	// round trips into a boolean or a date in any parser stricter than this
	// one, and Broadside's files should stay valid YAML for other tools.
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", "y", "n":
		return quote(s)
	}
	if looksNumeric(s) {
		return quote(s)
	}

	return s
}

// quote wraps a value in double quotes, escaping what has to be escaped.
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')

	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				// Remaining control characters get a numeric escape, which both
				// YAML and strconv.Unquote understand.
				fmt.Fprintf(&b, `\u%04x`, r)
				break
			}
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')
	return b.String()
}

// looksNumeric reports whether a bare value would be read as a number.
//
// The test is deliberately loose. Being wrong in the direction of quoting a
// value that did not need it costs two characters, while being wrong the other
// way turns a title into a float.
func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '+' || r == '-':
			if i != 0 {
				return false
			}
		case r == '.' || r == 'e' || r == 'E' || r == '_':
			// Underscores are digit separators in YAML numbers, and "e" covers
			// exponent notation.
		default:
			return false
		}
	}
	return hasDigit
}

// Validate reports whether the frontmatter has everything a post needs to be
// placed in the timeline.
//
// This is separate from parsing because the two happen at different moments. A
// file arriving over Syncthing is parsed first and reconciled against its own
// path second, so judging it complete before that reconciliation runs would
// reject files that are about to be perfectly valid.
func (fm Frontmatter) Validate() error {
	var missing []string

	if strings.TrimSpace(fm.Title) == "" {
		missing = append(missing, "title")
	}
	if fm.Slug == "" {
		missing = append(missing, "slug")
	}
	if fm.Published.IsZero() {
		missing = append(missing, "published")
	}

	if len(missing) > 0 {
		return fmt.Errorf("content: missing %s: %w", strings.Join(missing, ", "), ErrMissingRequiredField)
	}

	// A slug that is present but malformed is reported rather than silently
	// corrected, because correcting it would change a URL that may already be
	// published and linked to from elsewhere.
	if !IsValidSlug(fm.Slug) {
		return fmt.Errorf("content: slug %q is not in canonical form (lowercase letters, digits, and single hyphens)", fm.Slug)
	}

	return nil
}

// Reconcile fills in missing fields using the post's own path.
//
// The design treats frontmatter as the source of truth and the filename as
// derived from it, which is what lets a title change without breaking a URL.
// That leaves the reverse case, which is a file created by hand or by some
// other tool with an incomplete header. Rather than refuse to show it, the
// gaps are filled from the path, since the path already encodes both a date and
// a slug.
//
// Only absent fields are touched. Anything the author actually wrote wins,
// including a slug that disagrees with the filename, because the alternative is
// changing a published URL to match a file somebody renamed.
func (fm *Frontmatter) Reconcile(p PostPath, loc *time.Location) {
	if fm.Slug == "" {
		fm.Slug = p.Slug
	}

	if fm.Published.IsZero() {
		// Midnight in the site's location puts the post at the start of its
		// day. There is no better guess available, since the path records the
		// day and nothing finer.
		if loc == nil {
			loc = time.Local
		}
		fm.Published = time.Date(p.Year, p.Month, p.Day, 0, 0, 0, 0, loc)
	}

	if strings.TrimSpace(fm.Title) == "" {
		// Turning "first-light-on-the-sv503" into "First light on the sv503" is
		// a poor title, but it is better than a blank entry in the timeline,
		// and it is obvious enough that the author will notice and fix it.
		fm.Title = titleFromSlug(p.Slug)
	}
}

// titleFromSlug produces a readable placeholder title from a slug.
func titleFromSlug(slug string) string {
	if slug == "" {
		return "Untitled"
	}

	words := strings.ReplaceAll(slug, "-", " ")

	// Only the first letter is capitalized. Title casing every word would
	// require knowing which words to leave alone, and getting that subtly wrong
	// looks worse than plain sentence case.
	runes := []rune(words)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	return string(runes)
}
