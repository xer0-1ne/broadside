package content

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"simple title", "First Light on the SV503", "first-light-on-the-sv503"},
		{"already a slug", "already-a-slug", "already-a-slug"},
		{"leading and trailing space", "  Padded Title  ", "padded-title"},
		{"collapses punctuation runs", "Hello --- World", "hello-world"},
		{"collapses mixed separators", "One_Two/Three:Four", "one-two-three-four"},
		{"strips leading punctuation", "!!! Breaking News", "breaking-news"},
		{"strips trailing punctuation", "Are We Done???", "are-we-done"},
		{"keeps digits", "Top 10 Lenses of 2026", "top-10-lenses-of-2026"},
		{"apostrophes join words", "Don't Panic", "don-t-panic"},
		{"em dash becomes separator", "Before — After", "before-after"},
		{"emoji are dropped", "Launch Day 🚀 Recap", "launch-day-recap"},
		{"empty input", "", ""},
		{"only punctuation", "!!!???", ""},
		{"only whitespace", "   \t\n  ", ""},

		// Accent folding. These are the cases that justify the fold table
		// existing at all, since dropping the accented characters instead
		// would produce unreadable slugs.
		{"french accents", "Café au Lait", "cafe-au-lait"},
		{"german umlaut expands", "Münster", "muenster"},
		{"german eszett expands", "Straße", "strasse"},
		{"nordic characters", "Ærø Island", "aeroe-island"}, // "ø" folds to "oe" for the same reason "ö" does.
		{"polish characters", "Łódź Wrocław", "lodz-wroclaw"},
		{"czech carons", "Škoda Plzeň", "skoda-plzen"},
		{"turkish dotless i", "İstanbul", "istanbul"},
		{"spanish tilde", "El Niño", "el-nino"},

		// Scripts with no ASCII equivalent have nothing to fold to, so they
		// drop out entirely. Callers handle the empty result.
		{"chinese only", "北京", ""},
		{"mixed script keeps the latin part", "Tokyo 東京 Trip", "tokyo-trip"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Slugify(tc.input); got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSlugifyOutputIsAlwaysValid is the property that matters most, because the
// result becomes both a filename and a URL segment. Whatever goes in, what
// comes out must be safe in both places.
func TestSlugifyOutputIsAlwaysValid(t *testing.T) {
	inputs := []string{
		"../../etc/passwd",
		"..",
		"C:\\Windows\\System32",
		"post\x00.php",
		"<script>alert(1)</script>",
		"%2e%2e%2f",
		"a/b/c",
		"NUL",
		strings.Repeat("very long title ", 40),
		"🎉🎊🎈",
		"Ünïcödé Ëvërywhërë",
	}

	for _, input := range inputs {
		t.Run(input[:min(len(input), 24)], func(t *testing.T) {
			got := Slugify(input)
			if got == "" {
				return // An empty slug is a valid outcome; callers substitute a fallback.
			}
			if !IsValidSlug(got) {
				t.Errorf("Slugify(%q) produced %q, which IsValidSlug rejects", input, got)
			}
			for _, forbidden := range []string{"/", "\\", "..", "\x00", "<", ">", "%", ":"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("Slugify(%q) produced %q, which contains %q", input, got, forbidden)
				}
			}
		})
	}
}

// TestSlugifyRespectsLengthCap guards the filesystem limit, and specifically
// checks that a multi-character fold is never cut in half at the boundary.
func TestSlugifyRespectsLengthCap(t *testing.T) {
	long := strings.Repeat("ü", 200) // Each of these expands to two characters.
	got := Slugify(long)

	if len(got) > maxSlugLength {
		t.Errorf("slug is %d characters, want no more than %d", len(got), maxSlugLength)
	}
	// Every "ü" becomes "ue", so an odd count would mean an expansion was
	// truncated halfway through.
	if strings.Count(got, "ue")*2 != len(got) {
		t.Errorf("slug %q appears to contain a truncated fold", got)
	}
}

func TestSlugifyIsIdempotent(t *testing.T) {
	inputs := []string{
		"First Light on the SV503",
		"Café au Lait",
		"Hello --- World",
		"Straße",
	}

	for _, input := range inputs {
		once := Slugify(input)
		twice := Slugify(once)
		if once != twice {
			t.Errorf("Slugify(%q) = %q, but slugifying that again gives %q", input, once, twice)
		}
	}
}

func TestSlugifyWithFallback(t *testing.T) {
	if got := SlugifyWithFallback("北京", "2026-08-08"); got != "2026-08-08" {
		t.Errorf("got %q, want the fallback to be used for text that folds to nothing", got)
	}
	if got := SlugifyWithFallback("Real Title", "2026-08-08"); got != "real-title" {
		t.Errorf("got %q, want the fallback ignored when the title produces a slug", got)
	}
}

func TestIsValidSlug(t *testing.T) {
	valid := []string{"a", "first-post", "top-10-lenses-of-2026", "2026", "a-b-c"}
	for _, s := range valid {
		if !IsValidSlug(s) {
			t.Errorf("IsValidSlug(%q) = false, want true", s)
		}
	}

	invalid := []struct {
		slug   string
		reason string
	}{
		{"", "empty"},
		{"Has-Capitals", "uppercase is not canonical"},
		{"has spaces", "spaces are not allowed in a path component"},
		{"has_underscore", "underscore is outside the character set"},
		{"-leading", "a leading hyphen is never generated"},
		{"trailing-", "a trailing hyphen is never generated"},
		{"double--hyphen", "a doubled hyphen is never generated"},
		{"has/slash", "a slash would create a path segment"},
		{"has.dot", "a dot could be read as an extension"},
		{"café", "non-ASCII should have been folded already"},
		{strings.Repeat("a", maxSlugLength+1), "exceeds the length cap"},
	}
	for _, tc := range invalid {
		if IsValidSlug(tc.slug) {
			t.Errorf("IsValidSlug(%q) = true, want false because %s", tc.slug, tc.reason)
		}
	}
}
