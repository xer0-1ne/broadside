package render

import (
	"strings"
	"testing"
)

// TestDirectivesDoNotSwallowWhatFollowsThem covers a bug where a post lost
// everything after its directives.
//
// A container directive is opened by its header line and closed by a bare
// ":::". If the parser gets the closing wrong it keeps consuming, and the rest
// of the post disappears into a block that never ends. That failure is silent:
// the page renders, it is simply missing its second half, which is far worse
// than an error.
func TestDirectivesDoNotSwallowWhatFollowsThem(t *testing.T) {
	r := New()

	markdown := strings.Join([]string{
		`:::file{src="/uploads/spec.pdf" name="spec.pdf"}`,
		`:::`,
		``,
		`---`,
		``,
		`A paragraph after the directive.`,
	}, "\n")

	html, err := r.Render(markdown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(html, "A paragraph after the directive.") {
		t.Errorf("the paragraph after the directive was lost:\n%s", html)
	}
	if !strings.Contains(html, "bs-file") {
		t.Errorf("the file directive did not render:\n%s", html)
	}
}

// TestEveryDirectiveRenders checks the three container directives individually,
// since a failure in one is otherwise easy to miss among the others.
func TestEveryDirectiveRenders(t *testing.T) {
	r := New()

	cases := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "video",
			markdown: ":::video{src=\"/uploads/clip.mp4\"}\n:::",
			want:     "bs-video",
		},
		{
			name:     "file",
			markdown: ":::file{src=\"/uploads/spec.pdf\" name=\"spec.pdf\"}\n:::",
			want:     "bs-file",
		},
		{
			name:     "embed",
			markdown: ":::embed{url=\"https://example.com\" title=\"Example\"}\n:::",
			want:     "bs-embed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html, err := r.Render(tc.markdown)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(html, tc.want) {
				t.Errorf("expected %q in the output, got:\n%s", tc.want, html)
			}
		})
	}
}

// TestTablesSurvive is the other half of the same failure. The editor keeps a
// table as a passthrough block precisely so it reaches the renderer untouched,
// which only helps if the renderer then emits it.
func TestTablesSurvive(t *testing.T) {
	r := New()

	markdown := "| a | b |\n|---|---|\n| 1 | 2 |\n"

	html, err := r.Render(markdown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{"<table", "<th", "<td"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in the output, got:\n%s", want, html)
		}
	}
}

// TestDividerRenders checks the thematic break, which sits between the
// directives and the table in a real post and is the point the output was being
// cut off at.
func TestDividerRenders(t *testing.T) {
	r := New()

	html, err := r.Render("Before.\n\n---\n\nAfter.")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(html, "<hr") {
		t.Errorf("the divider did not render:\n%s", html)
	}
	if !strings.Contains(html, "After.") {
		t.Errorf("content after the divider was lost:\n%s", html)
	}
}

// TestGalleryRenders covers the carousel end to end, including that its markup
// survives the sanitizer.
//
// That last part is the reason this asserts on the container elements and not
// just the images. The gallery is generated after parsing and then passed
// through bluemonday like everything else, so an element the allowlist does not
// name is silently dropped and the slides collapse into a vertical stack. The
// page still renders, which is exactly why it needs a test.
func TestGalleryRenders(t *testing.T) {
	r := New()

	markdown := strings.Join([]string{
		`:::gallery{caption="First light"}`,
		`![Andromeda](/uploads/m31.jpg "Two hours")`,
		`![Triangulum](/uploads/m33.jpg)`,
		`:::`,
		``,
		`A paragraph after the gallery.`,
	}, "\n")

	html, err := r.Render(markdown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		`class="bs-gallery"`,
		`class="bs-gallery-track"`,
		`class="bs-gallery-slide"`,
		`/uploads/m31.jpg`,
		`/uploads/m33.jpg`,
		`alt="Andromeda"`,
		`data-caption="Two hours"`,
		`<figcaption>First light</figcaption>`,
		`A paragraph after the gallery.`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in the output, got:\n%s", want, html)
		}
	}

	// An uncaptioned slide falls back to the gallery's caption rather than
	// showing nothing in the lightbox.
	if !strings.Contains(html, `data-caption="First light"`) {
		t.Errorf("the second slide did not inherit the gallery caption:\n%s", html)
	}

	// Exactly two images, and no more. The directive body is a list of
	// markdown images, and goldmark will helpfully parse and render it a second
	// time unless the node declares itself raw, which puts every photograph on
	// the page twice: once in the carousel and once loose underneath it.
	if got := strings.Count(html, "<img"); got != 2 {
		t.Errorf("expected 2 images, got %d, which means the body was rendered twice:\n%s", got, html)
	}
}

// TestGalleryOfOneIsJustAnImage checks the degenerate case, where a carousel
// would have arrows that lead back to the slide you are already on.
func TestGalleryOfOneIsJustAnImage(t *testing.T) {
	r := New()

	html, err := r.Render(":::gallery{}\n![Only](/uploads/one.jpg)\n:::")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(html, "bs-gallery") {
		t.Errorf("a one-image gallery rendered as a carousel:\n%s", html)
	}
	if !strings.Contains(html, `class="bs-image"`) {
		t.Errorf("a one-image gallery did not render as a figure:\n%s", html)
	}
}

// TestGalleryRejectsHostileSources is the same scheme allowlist the rest of the
// package applies, checked here because the gallery reaches the output through
// its own code path rather than through the image transformer.
func TestGalleryRejectsHostileSources(t *testing.T) {
	r := New()

	markdown := strings.Join([]string{
		`:::gallery{}`,
		`![a](javascript:alert(1))`,
		`![b](/uploads/real.jpg)`,
		`![c](/uploads/other.jpg)`,
		`:::`,
	}, "\n")

	html, err := r.Render(markdown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(strings.ToLower(html), "javascript:") {
		t.Errorf("a javascript: source reached the output:\n%s", html)
	}
	if strings.Count(html, "bs-gallery-slide") != 2 {
		t.Errorf("expected the two good slides and nothing else, got:\n%s", html)
	}
}

// TestRawHTMLIsNeverRendered is the guarantee the whole package exists to
// provide, so it is worth asserting rather than assuming.
func TestRawHTMLIsNeverRendered(t *testing.T) {
	r := New()

	hostile := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<iframe src="https://evil.example"></iframe>`,
		`[click](javascript:alert(1))`,
		`<a href="javascript:alert(1)">click</a>`,
	}

	for _, markdown := range hostile {
		html, err := r.Render(markdown)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		lower := strings.ToLower(html)
		for _, forbidden := range []string{"<script", "<iframe", "onerror=", "javascript:"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("rendering %q produced %q, which contains %q", markdown, html, forbidden)
			}
		}
	}
}
