// Package render turns markdown into the HTML the site serves.
//
// Two rules govern everything here. The first is that raw HTML in a post is
// never rendered: goldmark is configured without html.WithUnsafe, so an author
// or an API client cannot inject markup. The second is that the output is
// passed through bluemonday anyway, which is redundant by design. The two
// defenses fail in different ways, and the cost of running both is a few
// microseconds per post against a class of vulnerability that is expensive to
// discover and embarrassing to ship.
package render

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// goldmarkMarkdown is an alias so the directive extension can reference the
// interface without importing goldmark itself, which would be a cycle.
type goldmarkMarkdown = goldmark.Markdown

// Renderer converts post bodies to HTML.
//
// It is safe for concurrent use and should be created once and shared. Both
// goldmark and the bluemonday policy are immutable after construction, and
// building a policy per request would be pure waste on the hottest path in the
// application.
type Renderer struct {
	markdown goldmark.Markdown
	policy   *bluemonday.Policy
}

// New creates a renderer.
func New() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			// Tables, strikethrough, task lists, and autolinks. These are what
			// people expect from markdown in 2026, and none of them introduce
			// raw HTML.
			extension.GFM,

			// Smart quotes and dashes. Purely typographic, and it makes prose
			// look considerably better for no authoring effort.
			extension.Typographer,

			&directiveExtension{},
		),
		goldmark.WithParserOptions(
			// Heading anchors, so a reader can link to a section. The IDs are
			// generated from the heading text by goldmark and are safe.
			parser.WithAutoHeadingID(),

			// Promotes an image sitting alone in a paragraph into a block-level
			// figure, which is what the lightbox attaches to. See image.go for
			// why this has to happen during transformation rather than at
			// render time.
			parser.WithASTTransformers(
				util.Prioritized(&imageTransformer{}, 100),
			),
		),
		goldmark.WithRendererOptions(
			// gmhtml.WithHardWraps is deliberately NOT set, so a single newline
			// continues the paragraph rather than breaking the line.
			//
			// The opposite setting is tempting, because it matches how someone
			// types into a web textarea. It is wrong here. These files are
			// meant to be edited in a text editor, where wrapping prose at
			// seventy or eighty columns is ordinary practice, and hard wraps
			// would render every one of those source line breaks as a visible
			// break. The result is ragged paragraphs that look broken to the
			// author and cannot be fixed without reflowing the file.
			//
			// An author who wants a genuine line break has the standard
			// markdown ways to ask for one: two trailing spaces, or a
			// backslash.

			// gmhtml.WithUnsafe is deliberately NOT set. Its absence is what
			// makes raw HTML in a post render as escaped text instead of as
			// markup, and it is the single most important line in this file.

			renderer.WithNodeRenderers(
				util.Prioritized(&directiveRenderer{}, 100),
				util.Prioritized(&imageFigureRenderer{}, 100),
			),
		),
	)

	return &Renderer{
		markdown: md,
		policy:   newPolicy(),
	}
}

// newPolicy builds the sanitizer allowlist.
//
// bluemonday works by permitting specific elements and attributes and dropping
// everything else, which means a tag nobody thought about is denied by default
// rather than allowed by oversight.
func newPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()

	// Heading anchors need their generated IDs to survive, since the fragment
	// links point at them.
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")

	// The lightbox needs its hooks. These are data attributes and classes on
	// figure and anchor elements, all of which are generated here rather than
	// authored, so permitting them does not widen what a post can express.
	p.AllowAttrs("class").Globally()
	p.AllowAttrs("data-lightbox", "data-full", "data-caption").OnElements("a", "figure", "img")

	// Media elements the directives produce. Without these the sanitizer would
	// strip exactly the markup this package just generated.
	p.AllowElements("figure", "figcaption", "video", "source", "picture")
	p.AllowAttrs("controls", "playsinline", "preload", "poster", "width", "height").OnElements("video")
	p.AllowAttrs("src", "type").OnElements("source")

	// Images need dimensions and lazy loading. Explicit width and height stop
	// the page from reflowing as photographs arrive, which is the single
	// biggest cause of the layout jumping while someone is reading.
	p.AllowAttrs("loading", "decoding", "width", "height").OnElements("img")

	// Relative URLs are what every internal link and uploaded image uses, and
	// the default policy rejects them.
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)

	// Only these schemes may appear in a link or an image. Excluding javascript
	// and data is the point: a "javascript:" href is a script execution path
	// that no amount of element filtering would catch.
	p.AllowURLSchemes("http", "https", "mailto")

	// Links that leave the site get rel="nofollow noopener noreferrer" and
	// target="_blank". noopener is the security-relevant one, since without it
	// the destination page can reach back through window.opener.
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RequireNoFollowOnFullyQualifiedLinks(true)
	p.RequireNoReferrerOnFullyQualifiedLinks(true)

	return p
}

// Render converts a markdown body to sanitized HTML.
func (r *Renderer) Render(markdown string) (string, error) {
	var buf bytes.Buffer
	if err := r.markdown.Convert([]byte(markdown), &buf); err != nil {
		return "", fmt.Errorf("render: converting markdown: %w", err)
	}
	return r.policy.Sanitize(buf.String()), nil
}

// Excerpt produces a short plain-text summary from a markdown body.
//
// This is used when a post has no explicit summary in its frontmatter. The
// markdown is rendered and then stripped of all markup rather than being
// truncated as raw text, which avoids cutting through the middle of a link and
// leaving bracket soup in the timeline.
func (r *Renderer) Excerpt(markdown string, maxLength int) string {
	if maxLength <= 0 {
		maxLength = 200
	}

	rendered, err := r.Render(markdown)
	if err != nil {
		return ""
	}

	// bluemonday with an empty policy strips every tag and leaves the text.
	text := bluemonday.StripTagsPolicy().Sanitize(rendered)
	text = html.UnescapeString(text)
	text = strings.Join(strings.Fields(text), " ")

	if len(text) <= maxLength {
		return text
	}

	// Cut at a word boundary so the excerpt does not end mid-word. Searching
	// back from the limit finds the last complete word that fits.
	cut := text[:maxLength]
	if space := strings.LastIndexByte(cut, ' '); space > maxLength/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// directiveRenderer emits HTML for the container directives.
type directiveRenderer struct{}

// RegisterFuncs wires the renderer to the directive node type.
func (r *directiveRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindDirective, r.render)
}

// render writes the HTML for one directive.
//
// Every attribute that reaches the output is escaped here rather than trusted.
// The values came from a post file, which on a multi-client setup means they
// came from an API request, so they are untrusted input even though the
// surrounding markup is generated.
func (r *directiveRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	directive, ok := node.(*Directive)
	if !ok {
		return ast.WalkContinue, nil
	}

	switch directive.Name {
	case "video":
		renderVideo(w, directive)
	case "file":
		renderFile(w, directive)
	case "embed":
		renderEmbed(w, directive)
	case "gallery":
		renderGallery(w, directive, source)
	}

	return ast.WalkContinue, nil
}

// renderVideo emits a self-hosted video player.
//
// No autoplay and no loop. A video that starts by itself in a timeline is
// hostile on a phone with a data cap, and preload="metadata" fetches only
// enough to show the duration rather than the whole file.
func renderVideo(w util.BufWriter, d *Directive) {
	src := safeURL(d.Attr("src"))
	if src == "" {
		return
	}

	fmt.Fprintf(w, `<figure class="bs-video">`)
	fmt.Fprintf(w, `<video controls playsinline preload="metadata"`)
	if poster := safeURL(d.Attr("poster")); poster != "" {
		fmt.Fprintf(w, ` poster=%q`, poster)
	}
	fmt.Fprintf(w, `><source src=%q type=%q>`, src, videoMIME(src))
	fmt.Fprintf(w, `Your browser cannot play this video. <a href=%q>Download it instead.</a>`, src)
	fmt.Fprintf(w, `</video>`)

	if caption := d.Attr("caption"); caption != "" {
		fmt.Fprintf(w, `<figcaption>%s</figcaption>`, html.EscapeString(caption))
	}
	fmt.Fprintf(w, `</figure>`)
}

// renderFile emits a download link for an attachment.
func renderFile(w util.BufWriter, d *Directive) {
	src := safeURL(d.Attr("src"))
	if src == "" {
		return
	}

	name := d.Attr("name")
	if name == "" {
		// Fall back to the last path segment, which is the original filename
		// in the storage layout.
		if slash := strings.LastIndexByte(src, '/'); slash >= 0 {
			name = src[slash+1:]
		} else {
			name = src
		}
	}

	fmt.Fprintf(w, `<figure class="bs-file"><a class="bs-file-link" href=%q download>`, src)
	fmt.Fprintf(w, `<span class="bs-file-name">%s</span>`, html.EscapeString(name))
	if size := formatBytes(d.Attr("size")); size != "" {
		fmt.Fprintf(w, `<span class="bs-file-size">%s</span>`, html.EscapeString(size))
	}
	fmt.Fprintf(w, `</a></figure>`)
}

// renderEmbed emits a link to external content.
//
// Deliberately a link and not an iframe. An iframe hands a third party a frame
// on the page, which brings their scripts, their cookies, and their tracking
// along with it, and it cannot be reconciled with a strict content security
// policy. If richer embedding is wanted later, fetching the target's metadata
// server-side and rendering a static preview card keeps the third party out of
// the reader's browser entirely.
func renderEmbed(w util.BufWriter, d *Directive) {
	target := safeURL(d.Attr("url"))
	if target == "" {
		return
	}

	label := d.Attr("title")
	if label == "" {
		label = target
	}

	fmt.Fprintf(w, `<figure class="bs-embed"><a href=%q rel="noopener noreferrer nofollow" target="_blank">%s</a></figure>`,
		target, html.EscapeString(label))
}

// safeURL validates a URL from a directive attribute and returns it escaped for
// use in an attribute, or an empty string if it is not acceptable.
//
// The scheme allowlist is the same one the sanitizer applies, repeated here
// because this markup is generated after parsing and the check has to happen
// before the value reaches the output rather than after.
func safeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	// A relative URL has no scheme and is what every uploaded file uses.
	if parsed.Scheme != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "mailto":
		default:
			// This is what rejects "javascript:alert(1)" and data URLs.
			return ""
		}
	}

	return html.EscapeString(parsed.String())
}

// videoMIME guesses a MIME type from a file extension.
//
// The source element needs a type for the browser to decide whether it can play
// the file without downloading it first. Guessing from the extension is
// acceptable here specifically because the upload path validates magic bytes
// before storing anything, so the extension is one Broadside assigned rather
// than one a client claimed.
func videoMIME(src string) string {
	lower := strings.ToLower(src)
	switch {
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".m4v"):
		return "video/mp4"
	case strings.HasSuffix(lower, ".webm"):
		return "video/webm"
	case strings.HasSuffix(lower, ".ogv"):
		return "video/ogg"
	case strings.HasSuffix(lower, ".mov"):
		// Served as mp4 because QuickTime containers usually hold H.264, which
		// browsers play, whereas the video/quicktime type makes them refuse.
		return "video/mp4"
	default:
		return "video/mp4"
	}
}

// formatBytes turns a byte count into something readable.
func formatBytes(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var size float64
	if _, err := fmt.Sscanf(raw, "%f", &size); err != nil || size <= 0 {
		return ""
	}

	// Decimal units rather than binary, because a file manager showing "2.4 MB"
	// is what the author will compare this against.
	units := []string{"B", "KB", "MB", "GB"}
	i := 0
	for size >= 1000 && i < len(units)-1 {
		size /= 1000
		i++
	}

	if i == 0 {
		return fmt.Sprintf("%.0f %s", size, units[i])
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}
