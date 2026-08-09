package content

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// chicago is used throughout these tests because a location with a non-zero
// offset is the only way to catch code that quietly assumes UTC.
func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("timezone database is not available: %v", err)
	}
	return loc
}

func TestSplit(t *testing.T) {
	source := "---\ntitle: Hello\n---\n\nBody text.\n"

	fm, body, err := Split(source)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if fm != "title: Hello" {
		t.Errorf("frontmatter = %q, want %q", fm, "title: Hello")
	}
	// The blank line separating the closing fence from the body is consumed,
	// because Marshal writes exactly one and the two have to be inverses.
	if body != "Body text.\n" {
		t.Errorf("body = %q, want %q", body, "Body text.\n")
	}
}

func TestSplitRejectsMalformedFiles(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   error
	}{
		{"no fence at all", "Just some text.\n", ErrNoFrontmatter},
		{"fence not on the first line", "\n---\ntitle: Hello\n---\n", ErrNoFrontmatter},
		{"opening fence never closed", "---\ntitle: Hello\n", ErrUnterminatedFrontmatter},
		{"empty file", "", ErrNoFrontmatter},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Split(tc.source); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestSplitHandlesEditorArtifacts covers the two things real files pick up that
// would otherwise produce a baffling "no frontmatter" error on a file that
// visibly has frontmatter.
func TestSplitHandlesEditorArtifacts(t *testing.T) {
	t.Run("byte order mark", func(t *testing.T) {
		source := "\ufeff---\ntitle: Hello\n---\n\nBody.\n"
		if _, _, err := Split(source); err != nil {
			t.Errorf("a leading byte order mark should be tolerated: %v", err)
		}
	})

	t.Run("windows line endings", func(t *testing.T) {
		source := "---\r\ntitle: Hello\r\n---\r\n\r\nBody.\r\n"
		fm, body, err := Split(source)
		if err != nil {
			t.Fatalf("Split: %v", err)
		}
		if fm != "title: Hello" {
			t.Errorf("frontmatter = %q, want the carriage returns normalized away", fm)
		}
		if strings.Contains(body, "\r") {
			t.Errorf("body = %q, want the carriage returns normalized away", body)
		}
	})
}

// TestSplitLeavesHorizontalRulesAlone checks that a "---" inside the body is not
// mistaken for the closing fence, which would truncate the post.
func TestSplitLeavesHorizontalRulesAlone(t *testing.T) {
	source := "---\ntitle: Hello\n---\n\nBefore.\n\n---\n\nAfter.\n"

	_, body, err := Split(source)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if !strings.Contains(body, "After.") {
		t.Errorf("body = %q, want it to include everything after the horizontal rule", body)
	}
}

func TestParsePost(t *testing.T) {
	loc := chicago(t)
	source := `---
title: First Light on the SV503
slug: first-light-on-the-sv503
published: 2026-08-08T14:30:22-05:00
updated: 2026-08-08T15:02:10-05:00
draft: false
tags: [astro, sv503]
summary: A first outing with the new scope.
cover: /uploads/2026/08/08/01-cover.jpg
---

Body text in plain markdown.
`

	post, err := ParsePost(source, loc)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}

	if post.Title != "First Light on the SV503" {
		t.Errorf("Title = %q", post.Title)
	}
	if post.Slug != "first-light-on-the-sv503" {
		t.Errorf("Slug = %q", post.Slug)
	}
	if want := time.Date(2026, 8, 8, 14, 30, 22, 0, loc); !post.Published.Equal(want) {
		t.Errorf("Published = %v, want %v", post.Published, want)
	}
	if post.Draft {
		t.Error("Draft = true, want false")
	}
	if !reflect.DeepEqual(post.Tags, []string{"astro", "sv503"}) {
		t.Errorf("Tags = %v", post.Tags)
	}
	if post.Summary != "A first outing with the new scope." {
		t.Errorf("Summary = %q", post.Summary)
	}
	if !strings.Contains(post.Body, "Body text in plain markdown.") {
		t.Errorf("Body = %q", post.Body)
	}
}

// TestUnknownKeysSurviveRoundTrip is the reason this parser is hand-written
// instead of a call into a YAML library. Anything Broadside does not recognize
// has to come back byte for byte, including nested structures it has no model
// for at all.
func TestUnknownKeysSurviveRoundTrip(t *testing.T) {
	loc := chicago(t)
	source := `---
title: A Post
slug: a-post
published: 2026-08-08T14:30:22-05:00
micro_syndicate_to: https://twitter.com/example
custom_nested:
  first: one
  second: two
custom_list:
  - alpha
  - beta
photo: ["https://example.com/a.jpg", "https://example.com/b.jpg"]
---

Body.
`

	post, err := ParsePost(source, loc)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}

	// Every unrecognized key should have been captured, in order.
	wantKeys := []string{"micro_syndicate_to", "custom_nested", "custom_list", "photo"}
	var gotKeys []string
	for _, extra := range post.Extra {
		gotKeys = append(gotKeys, extra.Key)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("preserved keys = %v, want %v", gotKeys, wantKeys)
	}

	// The nested structures have to come back with their indentation intact,
	// which is what a decode-and-reencode approach would destroy.
	rendered := post.Marshal()
	for _, fragment := range []string{
		"micro_syndicate_to: https://twitter.com/example",
		"custom_nested:\n  first: one\n  second: two",
		"custom_list:\n  - alpha\n  - beta",
		`photo: ["https://example.com/a.jpg", "https://example.com/b.jpg"]`,
	} {
		if !strings.Contains(rendered, fragment) {
			t.Errorf("the round trip lost or altered this fragment:\n%s\n\nfull output:\n%s", fragment, rendered)
		}
	}
}

// TestRoundTripIsStable checks that marshalling a parsed post and parsing it
// again produces the same value. Without this property, every save would drift
// the file a little further from what the author wrote.
func TestRoundTripIsStable(t *testing.T) {
	loc := chicago(t)
	sources := []string{
		"---\ntitle: Simple\nslug: simple\npublished: 2026-08-08T14:30:22-05:00\n---\n\nBody.\n",
		"---\ntitle: \"Quoted: With Colon\"\nslug: quoted\npublished: 2026-08-08T14:30:22-05:00\ndraft: true\ntags: [a, b, c]\n---\n\nBody.\n",
		"---\ntitle: No Body\nslug: no-body\npublished: 2026-08-08T14:30:22-05:00\n---\n",
		"---\ntitle: With Extras\nslug: with-extras\npublished: 2026-08-08T14:30:22-05:00\nweird_key: weird value\n---\n\nBody.\n",
	}

	for _, source := range sources {
		first, err := ParsePost(source, loc)
		if err != nil {
			t.Fatalf("parsing %q: %v", source, err)
		}

		rendered := first.Marshal()

		second, err := ParsePost(rendered, loc)
		if err != nil {
			t.Fatalf("reparsing rendered output %q: %v", rendered, err)
		}

		if !postsEqual(first, second) {
			t.Errorf("round trip changed the post:\n original: %+v\n reparsed: %+v\n rendered:\n%s", first, second, rendered)
		}

		// A second marshal must be byte identical to the first, or repeated
		// saves would keep producing diffs in a folder people keep in git.
		if again := second.Marshal(); again != rendered {
			t.Errorf("marshalling is not stable:\nfirst:\n%s\nsecond:\n%s", rendered, again)
		}
	}
}

func postsEqual(a, b Post) bool {
	return a.Title == b.Title &&
		a.Slug == b.Slug &&
		a.Published.Equal(b.Published) &&
		a.Updated.Equal(b.Updated) &&
		a.Draft == b.Draft &&
		reflect.DeepEqual(a.Tags, b.Tags) &&
		a.Summary == b.Summary &&
		a.Cover == b.Cover &&
		a.Body == b.Body &&
		reflect.DeepEqual(a.Extra, b.Extra)
}

// TestTitlesThatNeedQuoting covers the values that would parse back as
// something other than a string if they were written bare.
func TestTitlesThatNeedQuoting(t *testing.T) {
	loc := chicago(t)
	titles := []string{
		"Postgres: A Retrospective", // A colon would split the key.
		"true",                      // Would come back as a boolean.
		"false",
		"null",
		"2026",              // Would come back as a number.
		"3.14",              // Likewise.
		"-1",                // Leading dash reads as a list item.
		"- not a list item", // Same.
		"#hashtag",          // Leading hash reads as a comment.
		"Ends with a colon:",
		"has # a comment marker",
		"  leading and trailing  ",
		"[brackets]",
		"{braces}",
		"*star",
		"&anchor",
		"!bang",
		"@at",
		"%percent",
		"|pipe",
		">gt",
		`"already quoted"`,
		"'single quoted'",
		"yes",
		"no",
		"on",
		"off",
	}

	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			original := Post{
				Frontmatter: Frontmatter{
					Title:     title,
					Slug:      "a-slug",
					Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
				},
				Body: "Body.\n",
			}

			reparsed, err := ParsePost(original.Marshal(), loc)
			if err != nil {
				t.Fatalf("reparsing:\n%s\nerror: %v", original.Marshal(), err)
			}
			if reparsed.Title != title {
				t.Errorf("title %q came back as %q\nrendered:\n%s", title, reparsed.Title, original.Marshal())
			}
		})
	}
}

func TestParseTimeAcceptsHandWrittenForms(t *testing.T) {
	loc := chicago(t)

	cases := []struct {
		input string
		want  time.Time
	}{
		{"2026-08-08T14:30:22-05:00", time.Date(2026, 8, 8, 14, 30, 22, 0, loc)},
		{"2026-08-08T14:30:22", time.Date(2026, 8, 8, 14, 30, 22, 0, loc)},
		{"2026-08-08 14:30:22", time.Date(2026, 8, 8, 14, 30, 22, 0, loc)},
		{"2026-08-08 14:30", time.Date(2026, 8, 8, 14, 30, 0, 0, loc)},
		{"2026-08-08", time.Date(2026, 8, 8, 0, 0, 0, 0, loc)},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseTime(tc.input, loc)
			if err != nil {
				t.Fatalf("ParseTime(%q): %v", tc.input, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("ParseTime(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseTimeUsesTheSiteLocation is the specific bug this guards against: a
// timestamp with no offset must not be read as UTC, or every hand-written date
// shifts by the author's distance from Greenwich.
func TestParseTimeUsesTheSiteLocation(t *testing.T) {
	loc := chicago(t)

	got, err := ParseTime("2026-08-08 14:30:00", loc)
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}

	if _, offset := got.Zone(); offset == 0 {
		t.Error("the timestamp was parsed as UTC, want the site's location applied")
	}
	if got.UTC().Hour() != 19 {
		t.Errorf("2:30pm in Chicago is 7:30pm UTC, got %v", got.UTC())
	}
}

func TestParseTimeRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "not a date", "08/08/2026", "2026-13-45"} {
		if _, err := ParseTime(input, time.UTC); err == nil {
			t.Errorf("ParseTime(%q) succeeded, want a rejection", input)
		}
	}
}

func TestParseTagsInBothForms(t *testing.T) {
	loc := chicago(t)

	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "flow form with brackets",
			source: "tags: [astro, sv503]",
			want:   []string{"astro", "sv503"},
		},
		{
			name:   "flow form without brackets",
			source: "tags: astro, sv503",
			want:   []string{"astro", "sv503"},
		},
		{
			name:   "block form",
			source: "tags:\n  - astro\n  - sv503",
			want:   []string{"astro", "sv503"},
		},
		{
			name:   "block form at the same indentation",
			source: "tags:\n- astro\n- sv503",
			want:   []string{"astro", "sv503"},
		},
		{
			name:   "tags are slugified",
			source: "tags: [Astro Photography, SV503]",
			want:   []string{"astro-photography", "sv503"},
		},
		{
			name:   "duplicates are removed",
			source: "tags: [astro, Astro, ASTRO]",
			want:   []string{"astro"},
		},
		{
			name:   "quoted tags",
			source: `tags: ["astro", 'sv503']`,
			want:   []string{"astro", "sv503"},
		},
		{
			name:   "empty list",
			source: "tags: []",
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, err := ParseFrontmatter(tc.source, loc)
			if err != nil {
				t.Fatalf("ParseFrontmatter: %v", err)
			}
			if !reflect.DeepEqual(fm.Tags, tc.want) {
				t.Errorf("Tags = %v, want %v", fm.Tags, tc.want)
			}
		})
	}
}

func TestParseBoolSpellings(t *testing.T) {
	loc := chicago(t)

	truthy := []string{"true", "True", "TRUE", "yes", "on", "y", "1"}
	for _, value := range truthy {
		fm, err := ParseFrontmatter("draft: "+value, loc)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}
		if !fm.Draft {
			t.Errorf("draft: %s parsed as false, want true", value)
		}
	}

	// Anything unrecognized is false, which for the draft flag is the safe
	// direction: a typo leaves the post visible rather than silently hidden.
	falsy := []string{"false", "no", "off", "0", "", "maybe", "nope"}
	for _, value := range falsy {
		fm, err := ParseFrontmatter("draft: "+value, loc)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}
		if fm.Draft {
			t.Errorf("draft: %q parsed as true, want false", value)
		}
	}
}

func TestValidate(t *testing.T) {
	loc := chicago(t)
	complete := Frontmatter{
		Title:     "A Post",
		Slug:      "a-post",
		Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
	}

	if err := complete.Validate(); err != nil {
		t.Errorf("a complete frontmatter was rejected: %v", err)
	}

	t.Run("missing fields are named", func(t *testing.T) {
		err := Frontmatter{}.Validate()
		if !errors.Is(err, ErrMissingRequiredField) {
			t.Fatalf("got %v, want ErrMissingRequiredField", err)
		}
		for _, field := range []string{"title", "slug", "published"} {
			if !strings.Contains(err.Error(), field) {
				t.Errorf("error %q does not mention the missing %q field", err, field)
			}
		}
	})

	t.Run("a malformed slug is reported rather than corrected", func(t *testing.T) {
		bad := complete
		bad.Slug = "Not A Valid Slug"
		if err := bad.Validate(); err == nil {
			t.Error("a malformed slug was accepted")
		}
	})
}

// TestReconcileFillsGapsFromThePath covers files created by hand or by another
// tool, which is a case the flat-file design invites rather than an edge case.
func TestReconcileFillsGapsFromThePath(t *testing.T) {
	loc := chicago(t)
	p := PostPath{Year: 2026, Month: time.August, Day: 8, Sequence: 1, Slug: "hand-written-note"}

	t.Run("an empty header is filled entirely", func(t *testing.T) {
		var fm Frontmatter
		fm.Reconcile(p, loc)

		if fm.Slug != "hand-written-note" {
			t.Errorf("Slug = %q, want it taken from the path", fm.Slug)
		}
		if fm.Title != "Hand written note" {
			t.Errorf("Title = %q, want a readable placeholder from the slug", fm.Title)
		}
		want := time.Date(2026, 8, 8, 0, 0, 0, 0, loc)
		if !fm.Published.Equal(want) {
			t.Errorf("Published = %v, want %v", fm.Published, want)
		}
		if err := fm.Validate(); err != nil {
			t.Errorf("the reconciled frontmatter is still invalid: %v", err)
		}
	})

	t.Run("existing values are never overwritten", func(t *testing.T) {
		published := time.Date(2026, 8, 8, 9, 15, 0, 0, loc)
		fm := Frontmatter{
			Title: "The Real Title",
			// A slug that disagrees with the filename wins, because changing it
			// would break a URL that may already be published.
			Slug:      "the-original-slug",
			Published: published,
		}
		fm.Reconcile(p, loc)

		if fm.Title != "The Real Title" {
			t.Errorf("Title = %q, want it left alone", fm.Title)
		}
		if fm.Slug != "the-original-slug" {
			t.Errorf("Slug = %q, want the frontmatter to win over the filename", fm.Slug)
		}
		if !fm.Published.Equal(published) {
			t.Errorf("Published = %v, want it left alone", fm.Published)
		}
	})
}

// TestMarshalOmitsEmptyOptionalFields keeps stored files tidy. A file full of
// blank keys is noise in a format people are meant to read.
func TestMarshalOmitsEmptyOptionalFields(t *testing.T) {
	loc := chicago(t)
	post := Post{
		Frontmatter: Frontmatter{
			Title:     "Minimal",
			Slug:      "minimal",
			Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
		},
		Body: "Body.\n",
	}

	rendered := post.Marshal()
	for _, key := range []string{"updated:", "draft:", "tags:", "summary:", "cover:"} {
		if strings.Contains(rendered, key) {
			t.Errorf("rendered output contains %q for an empty field:\n%s", key, rendered)
		}
	}
}

// TestMarshalFieldOrderIsFixed guards against files whose lines shuffle between
// saves, which would produce noisy diffs in a content folder kept in git.
func TestMarshalFieldOrderIsFixed(t *testing.T) {
	loc := chicago(t)
	post := Post{
		Frontmatter: Frontmatter{
			Title:     "Ordered",
			Slug:      "ordered",
			Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
			Updated:   time.Date(2026, 8, 8, 15, 0, 0, 0, loc),
			Draft:     true,
			Tags:      []string{"a", "b"},
			Summary:   "A summary.",
			Cover:     "/uploads/2026/08/08/01-c.jpg",
		},
		Body: "Body.\n",
	}

	rendered := post.Marshal()
	wantOrder := []string{"title:", "slug:", "published:", "updated:", "draft:", "tags:", "summary:", "cover:"}

	position := 0
	for _, key := range wantOrder {
		found := strings.Index(rendered[position:], key)
		if found < 0 {
			t.Fatalf("%q is missing or out of order in:\n%s", key, rendered)
		}
		position += found
	}
}

// TestBodyIsPreservedExactly matters because the body is the actual writing.
// Any normalization applied to it is a change the author did not ask for.
func TestBodyIsPreservedExactly(t *testing.T) {
	loc := chicago(t)
	body := "First paragraph.\n\n    indented code block\n\n> A quote.\n\n- list item\n- another\n\n\n\nTrailing blank lines above.\n"

	post := Post{
		Frontmatter: Frontmatter{
			Title:     "Body Test",
			Slug:      "body-test",
			Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
		},
		Body: body,
	}

	reparsed, err := ParsePost(post.Marshal(), loc)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}
	if reparsed.Body != body {
		t.Errorf("body changed:\n got: %q\nwant: %q", reparsed.Body, body)
	}
}

// TestBlockDirectivesSurviveTheBody confirms the container directives from the
// design notes pass through untouched. They are rendered later, but the parser
// must not treat them as anything special.
func TestBlockDirectivesSurviveTheBody(t *testing.T) {
	loc := chicago(t)
	body := `Some text.

:::video{src="/uploads/2026/08/08/01-clip.mp4" poster="/uploads/2026/08/08/02-poster.jpg"}
:::

:::file{src="/uploads/2026/08/08/03-spec.pdf" name="spec.pdf" size="2400000"}
:::
`

	post := Post{
		Frontmatter: Frontmatter{
			Title:     "Directives",
			Slug:      "directives",
			Published: time.Date(2026, 8, 8, 14, 30, 22, 0, loc),
		},
		Body: body,
	}

	reparsed, err := ParsePost(post.Marshal(), loc)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}
	if reparsed.Body != body {
		t.Errorf("the directives were altered:\n got: %q\nwant: %q", reparsed.Body, body)
	}
}

func TestCommentsAndBlankLinesInFrontmatter(t *testing.T) {
	loc := chicago(t)
	source := `---
# This is a comment.
title: A Post

slug: a-post
published: 2026-08-08T14:30:22-05:00
---

Body.
`

	post, err := ParsePost(source, loc)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}
	if post.Title != "A Post" || post.Slug != "a-post" {
		t.Errorf("comments or blank lines interfered with parsing: %+v", post.Frontmatter)
	}
}
