package config

import (
	"strings"
	"time"
)

// Date formatting uses a tiny purpose-built language rather than Go's reference
// layout or strftime.
//
// Go's layout, where the format is spelled with the date "Mon Jan 2 15:04:05
// MST 2006", is genuinely hard to use for anyone who has not memorised it, and
// this setting is edited by the site's author rather than by a programmer.
// strftime is more familiar but has around forty specifiers, most of which mean
// nothing on a blog post byline.
//
// So there are six letters and a list of characters permitted between them.
// Everything a person actually wants from a post date is expressible, and the
// whole thing fits in the help text beside the field:
//
//	M  month spelled out       August
//	m  month abbreviated       Aug
//	D  day, no leading zero    6
//	d  day, leading zero       06
//	Y  four-digit year         2026
//	y  two-digit year          26
//
// Anything in the divider set below passes through as written, so "D, M Y" and
// "y/m/d" and "d - M. - Y" all work. Any other character is dropped rather than
// printed, which keeps a stray letter from being mistaken for a token nobody
// defined.

// DefaultDateFormat is what a new site uses.
//
// Month spelled out, because a blog post is read by people rather than parsed,
// and an unambiguous month avoids the day-month ordering confusion that numeric
// dates cause between one country and another.
const DefaultDateFormat = "M D, Y"

// dateDividers is the set of characters allowed between tokens.
//
// This is an allowlist rather than "anything that is not a token letter",
// because the latter would let a format silently include a letter that later
// becomes a token and change every date on the site.
const dateDividers = " -,|_:;./\\"

// FormatDate renders a time using the site's format string.
//
// An empty format falls back to the default rather than producing an empty
// date, since a post with no visible date reads as a rendering bug.
func FormatDate(t time.Time, format string) string {
	// A format with no token in it renders as its dividers, so "---" would put
	// three hyphens where a date belongs. Checking for a token rather than only
	// for empty output catches that.
	//
	// Config values have already been through applyFallbacks, so this is
	// belt and braces, and it is what makes the function safe to call with a
	// value from anywhere.
	if !ValidDateFormat(format) {
		format = DefaultDateFormat
	}

	var b strings.Builder
	b.Grow(len(format) + 8)

	for _, r := range format {
		switch r {
		case 'M':
			b.WriteString(t.Format("January"))
		case 'm':
			b.WriteString(t.Format("Jan"))
		case 'D':
			b.WriteString(t.Format("2"))
		case 'd':
			b.WriteString(t.Format("02"))
		case 'Y':
			b.WriteString(t.Format("2006"))
		case 'y':
			b.WriteString(t.Format("06"))
		default:
			// Dividers pass through as typed. Anything else is dropped, so a
			// format cannot smuggle arbitrary text into every byline.
			if strings.ContainsRune(dateDividers, r) {
				b.WriteRune(r)
			}
		}
	}

	return strings.TrimSpace(b.String())
}

// ValidDateFormat reports whether a format string contains at least one token
// and no characters outside the accepted set.
//
// This is for the settings form, so the author is told their format is wrong
// while they are looking at it, rather than discovering it later on a post.
func ValidDateFormat(format string) bool {
	if strings.TrimSpace(format) == "" {
		return false
	}

	hasToken := false
	for _, r := range format {
		switch r {
		case 'M', 'm', 'D', 'd', 'Y', 'y':
			hasToken = true
		default:
			if !strings.ContainsRune(dateDividers, r) {
				return false
			}
		}
	}
	return hasToken
}

// DateFormatExamples renders a set of formats against one date, for the help
// text beside the field.
//
// Showing the result is worth more than describing the rule. Somebody choosing
// a date format wants to see "6 Aug 2026", not read that lowercase m is the
// abbreviated month.
func DateFormatExamples() []DateExample {
	// A fixed date rather than today's, so the examples do not change under the
	// reader and so a two-digit day is visibly different from a one-digit one.
	sample := time.Date(2026, time.August, 6, 14, 30, 0, 0, time.UTC)

	formats := []string{
		"M D, Y",
		"D m Y",
		"d/m/Y",
		"m d, Y",
		"D-M-y",
		"Y.m.d",
	}

	examples := make([]DateExample, 0, len(formats))
	for _, format := range formats {
		examples = append(examples, DateExample{
			Format: format,
			Result: FormatDate(sample, format),
		})
	}
	return examples
}

// DateExample pairs a format with what it produces.
type DateExample struct {
	Format string
	Result string
}
