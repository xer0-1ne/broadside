package content

import "strings"

// maxSlugLength caps how long a generated slug can be.
//
// The limit exists for filesystem reasons rather than aesthetic ones. Most
// filesystems cap a single path component at 255 bytes, and the stored filename
// carries a sequence prefix and an extension on top of the slug. Eighty
// characters leaves generous headroom while still producing URLs that survive
// being pasted into a chat window without wrapping.
const maxSlugLength = 80

// asciiFold maps accented Latin characters to their unaccented equivalents.
//
// The obvious alternative is golang.org/x/text, which does this properly for
// every script through Unicode normalization. It was rejected because its
// normalization tables add several hundred kilobytes to a binary whose entire
// pitch is being small and self-contained, and because the actual requirement
// here is narrow: fold the accented Latin characters a European-language blog
// title will contain so that "Café Münster" does not become "caf-nster".
//
// The table covers Latin-1 Supplement and Latin Extended-A, which together
// account for essentially every Western and Central European language. Anything
// outside it falls through to the separator branch in Slugify, which is the
// correct outcome for scripts that have no meaningful ASCII equivalent.
//
// Multi-character expansions are intentional. German "ß" conventionally
// transliterates to "ss" and "ä" to "ae" in URLs, and the Nordic "æ" and "ø"
// behave the same way. Producing a single character for these would be shorter
// but wrong.
var asciiFold = map[rune]string{
	// Latin-1 Supplement.
	'À': "a", 'Á': "a", 'Â': "a", 'Ã': "a", 'Ä': "ae", 'Å': "a", 'Æ': "ae",
	'Ç': "c", 'È': "e", 'É': "e", 'Ê': "e", 'Ë': "e",
	'Ì': "i", 'Í': "i", 'Î': "i", 'Ï': "i",
	'Ð': "d", 'Ñ': "n",
	'Ò': "o", 'Ó': "o", 'Ô': "o", 'Õ': "o", 'Ö': "oe", 'Ø': "oe",
	'Ù': "u", 'Ú': "u", 'Û': "u", 'Ü': "ue",
	'Ý': "y", 'Þ': "th", 'ß': "ss",
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "ae", 'å': "a", 'æ': "ae",
	'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ð': "d", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "oe", 'ø': "oe",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "ue",
	'ý': "y", 'þ': "th", 'ÿ': "y",

	// Latin Extended-A. This is what covers Polish, Czech, Hungarian,
	// Romanian, Turkish, Croatian, and the Baltic languages.
	'Ā': "a", 'ā': "a", 'Ă': "a", 'ă': "a", 'Ą': "a", 'ą': "a",
	'Ć': "c", 'ć': "c", 'Ĉ': "c", 'ĉ': "c", 'Ċ': "c", 'ċ': "c", 'Č': "c", 'č': "c",
	'Ď': "d", 'ď': "d", 'Đ': "d", 'đ': "d",
	'Ē': "e", 'ē': "e", 'Ĕ': "e", 'ĕ': "e", 'Ė': "e", 'ė': "e",
	'Ę': "e", 'ę': "e", 'Ě': "e", 'ě': "e",
	'Ĝ': "g", 'ĝ': "g", 'Ğ': "g", 'ğ': "g", 'Ġ': "g", 'ġ': "g", 'Ģ': "g", 'ģ': "g",
	'Ĥ': "h", 'ĥ': "h", 'Ħ': "h", 'ħ': "h",
	'Ĩ': "i", 'ĩ': "i", 'Ī': "i", 'ī': "i", 'Ĭ': "i", 'ĭ': "i",
	'Į': "i", 'į': "i", 'İ': "i", 'ı': "i",
	'Ĳ': "ij", 'ĳ': "ij", 'Ĵ': "j", 'ĵ': "j",
	'Ķ': "k", 'ķ': "k", 'ĸ': "k",
	'Ĺ': "l", 'ĺ': "l", 'Ļ': "l", 'ļ': "l", 'Ľ': "l", 'ľ': "l",
	'Ŀ': "l", 'ŀ': "l", 'Ł': "l", 'ł': "l",
	'Ń': "n", 'ń': "n", 'Ņ': "n", 'ņ': "n", 'Ň': "n", 'ň': "n", 'ŉ': "n",
	'Ŋ': "n", 'ŋ': "n",
	'Ō': "o", 'ō': "o", 'Ŏ': "o", 'ŏ': "o", 'Ő': "o", 'ő': "o", 'Œ': "oe", 'œ': "oe",
	'Ŕ': "r", 'ŕ': "r", 'Ŗ': "r", 'ŗ': "r", 'Ř': "r", 'ř': "r",
	'Ś': "s", 'ś': "s", 'Ŝ': "s", 'ŝ': "s", 'Ş': "s", 'ş': "s", 'Š': "s", 'š': "s",
	'Ţ': "t", 'ţ': "t", 'Ť': "t", 'ť': "t", 'Ŧ': "t", 'ŧ': "t",
	'Ũ': "u", 'ũ': "u", 'Ū': "u", 'ū': "u", 'Ŭ': "u", 'ŭ': "u",
	'Ů': "u", 'ů': "u", 'Ű': "u", 'ű': "u", 'Ų': "u", 'ų': "u",
	'Ŵ': "w", 'ŵ': "w", 'Ŷ': "y", 'ŷ': "y", 'Ÿ': "y",
	'Ź': "z", 'ź': "z", 'Ż': "z", 'ż': "z", 'Ž': "z", 'ž': "z",
}

// Slugify converts arbitrary text into the restricted character set used for
// URLs and filenames, which is lowercase ASCII letters, digits, and hyphens.
//
// The output is deliberately narrow. Because a slug becomes both a URL segment
// and a path component, every character that survives has to be safe in both
// contexts, and the intersection of "safe in a URL" and "safe on every
// filesystem we might run on" is small. Restricting the output rather than
// escaping the input means the storage layer never has to reason about encoding
// at all.
//
// Text with no ASCII equivalent, such as Chinese or Arabic, has nothing to fold
// to and is removed. That can leave an empty result, which callers have to
// handle; SlugifyWithFallback is the usual answer.
func Slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	// pendingHyphen defers writing a separator until a character that needs one
	// actually appears. This collapses runs of spaces and punctuation into a
	// single hyphen and drops trailing separators without a second cleanup
	// pass, so "hello --- world" and "hello world" produce the same slug.
	pendingHyphen := false

	// writeSeparator is called before appending real characters. The b.Len()
	// guard is what stops a leading hyphen when the input starts with
	// punctuation.
	writeSeparator := func() {
		if pendingHyphen && b.Len() > 0 {
			b.WriteByte('-')
		}
		pendingHyphen = false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			writeSeparator()
			b.WriteRune(r)

		case r >= 'A' && r <= 'Z':
			writeSeparator()
			b.WriteRune(r + ('a' - 'A'))

		default:
			if folded, ok := asciiFold[r]; ok {
				writeSeparator()
				b.WriteString(folded)
				break
			}
			// Everything else, including spaces, punctuation, emoji, and any
			// script the fold table does not cover, becomes a separator.
			if b.Len() > 0 {
				pendingHyphen = true
			}
		}

		// Stop early once the cap is reached. A separator and a character can
		// be written together, so this can overshoot slightly, which the hard
		// truncation below corrects.
		if b.Len() >= maxSlugLength {
			break
		}
	}

	slug := strings.Trim(b.String(), "-")

	// Enforce the cap exactly. Without this the loop above can overshoot by a
	// byte or two, producing a slug that IsValidSlug then rejects, and the
	// guarantee that generated output is always valid is worth more than the
	// last character of a very long title.
	//
	// Slicing by byte is safe here specifically because everything written
	// above is ASCII, either from the a-z0-9 branches or from the fold table,
	// so there is no multi-byte character to cut in half.
	if len(slug) > maxSlugLength {
		slug = strings.TrimRight(slug[:maxSlugLength], "-")
	}

	return slug
}

// SlugifyWithFallback behaves like Slugify but substitutes fallback when the
// input contains nothing that survives the character filter.
//
// This is the form callers should reach for when writing a file, because a post
// titled entirely in a non-Latin script would otherwise produce an empty slug
// and therefore a filename that is nothing but a sequence number and an
// extension. The fallback is typically derived from the date, which keeps the
// URL meaningless but valid rather than broken.
func SlugifyWithFallback(s, fallback string) string {
	if slug := Slugify(s); slug != "" {
		return slug
	}
	return fallback
}

// IsValidSlug reports whether s is already in the canonical form Slugify
// produces.
//
// Validation is kept separate from generation because the two answer different
// questions. Generation applies to a title the author typed. Validation applies
// to a slug that arrived in frontmatter, possibly written by hand, where
// silently rewriting it would change a published URL without telling anyone. A
// slug that fails this check should be reported to the author, not corrected
// behind their back.
func IsValidSlug(s string) bool {
	if s == "" || len(s) > maxSlugLength {
		return false
	}
	// Leading and trailing hyphens are rejected because they are almost always
	// a mistake, and because allowing them would mean two visually similar
	// slugs could differ only by a character nobody notices.
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	// A doubled hyphen is likewise never something Slugify produces, so
	// accepting it would create slugs that cannot be regenerated from their own
	// title.
	if strings.Contains(s, "--") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
