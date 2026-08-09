package render

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// This file teaches goldmark the container directive syntax from the design
// notes:
//
//	:::video{src="/uploads/2026/08/08/01-clip.mp4" poster="/uploads/.../02.jpg"}
//	:::
//
// Markdown has no syntax for a video, a file attachment, or an embed, and every
// platform invents something to cover the gap. The alternatives were considered
// and rejected. Raw HTML in the source defeats the sanitizer and makes the file
// dangerous to accept from an API client. Gutenberg-style HTML comment
// delimiters work but make the raw file ugly, which undermines the entire
// premise of a format people edit by hand. Container directives are a
// well-established convention, they read cleanly in a text editor, and they are
// unambiguous to parse.
//
// The syntax surface is small enough that a purpose-built parser is less code
// than adapting a general directive extension, and it means the accepted set of
// block types is defined here rather than inherited from someone else's idea of
// what a directive can be.

// directiveMarker opens and closes a directive block.
const directiveMarker = ":::"

// Kind identifiers registered with goldmark so the renderer can dispatch on
// node type.
var KindDirective = ast.NewNodeKind("Directive")

// Directive is a parsed container directive.
type Directive struct {
	ast.BaseBlock

	// Name is the directive type, such as "video" or "file".
	Name string

	// Attrs holds the key/value pairs from the brace section. It is not
	// named Attributes because ast.Node already declares a method by that
	// name, and a field would shadow it and break the interface.
	Attrs map[string]string
}

// Dump satisfies ast.Node, and exists for goldmark's debugging output.
func (d *Directive) Dump(source []byte, level int) {
	ast.DumpHelper(d, source, level, nil, nil)
}

// Kind identifies the node type to goldmark's renderer.
func (d *Directive) Kind() ast.NodeKind { return KindDirective }

// IsRaw stops goldmark from parsing the directive's body as inline markdown.
//
// This is load-bearing, and its absence fails in a way that is obvious on the
// page but not at all obvious in the code. goldmark runs an inline pass over
// the lines of every block that does not declare itself raw, attaches the
// result as children, and renders them. A gallery's body is a list of markdown
// images, so without this the five images render inside the carousel and then
// again as five loose images immediately after it.
//
// The body belongs to this package, which parses it itself in gallery.go.
func (d *Directive) IsRaw() bool { return true }

// Attr returns an attribute value, or an empty string when absent.
func (d *Directive) Attr(key string) string {
	if d.Attrs == nil {
		return ""
	}
	return d.Attrs[key]
}

// Body returns the directive's inner lines with their trailing newlines
// removed. Only the gallery directive has anything inside it to read.
func (d *Directive) Body(source []byte) []string {
	lines := d.Lines()
	out := make([]string, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		out = append(out, strings.TrimRight(string(segment.Value(source)), "\r\n"))
	}
	return out
}

// directiveParser recognizes container directive blocks.
type directiveParser struct{}

// Trigger tells goldmark which byte can begin this block, so the parser is only
// consulted on lines that could possibly match.
func (p *directiveParser) Trigger() []byte { return []byte{':'} }

// Open begins a directive block when the line starts one.
func (p *directiveParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	trimmed := strings.TrimSpace(string(line))

	if !strings.HasPrefix(trimmed, directiveMarker) {
		return nil, parser.NoChildren
	}

	rest := strings.TrimPrefix(trimmed, directiveMarker)
	if rest == "" {
		// A bare marker is a closing fence, which the open handler should not
		// consume. Leaving it unmatched means a stray closer renders as plain
		// text rather than opening an empty block.
		return nil, parser.NoChildren
	}

	name, attributes := parseDirectiveHeader(rest)
	if name == "" || !allowedDirectives[name] {
		// An unrecognized directive is left alone so it renders as ordinary
		// text. Silently swallowing it would make a typo invisible, and the
		// author is better served by seeing their mistake on the page.
		return nil, parser.NoChildren
	}

	advanceLine(reader, segment)

	return &Directive{Name: name, Attrs: attributes}, parser.NoChildren
}

// advanceLine consumes a line without consuming the newline that ends it.
//
// This is the detail that decides whether a directive closes. PeekLine returns
// the line including its trailing newline, so advancing by len(line) eats a
// byte the parser still needs, and its line tracking slips by one. The symptom
// is not a parse error: the block simply never closes, and the rest of the post
// is swallowed into it and disappears from the page.
//
// goldmark's own fenced code parser does exactly this, for exactly this reason.
func advanceLine(reader text.Reader, segment text.Segment) {
	length := segment.Stop - segment.Start - segment.Padding
	if length > 0 {
		// The final byte is the newline, which the parser consumes itself.
		reader.Advance(length - 1)
	}
}

// Continue consumes lines until the closing marker.
//
// Every line before the closer is kept on the node. Three of the four
// directives carry all their data in attributes and never look at it, and the
// fourth, gallery, is a list of images that belongs on its own lines rather
// than crammed into one attribute. Keeping the lines either way costs a slice
// append and means a directive written with a blank line inside it still
// closes properly.
func (p *directiveParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	line, segment := reader.PeekLine()

	// End of document without a closing marker. Closing the block anyway is
	// more forgiving than discarding it, and it matches how markdown treats an
	// unterminated code fence.
	if len(line) == 0 {
		return parser.Close
	}

	if strings.TrimSpace(string(line)) == directiveMarker {
		advanceLine(reader, segment)
		return parser.Close
	}

	node.Lines().Append(segment)
	advanceLine(reader, segment)
	return parser.Continue | parser.NoChildren
}

// Close finalizes the block. Nothing is needed, since all the data was captured
// when the block opened.
func (p *directiveParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

// CanInterruptParagraph allows a directive to start immediately after a line of
// text with no blank line between them, which is what an author writing quickly
// will produce.
func (p *directiveParser) CanInterruptParagraph() bool { return true }

// CanAcceptIndentedLine keeps an indented directive from being parsed as a code
// block instead.
func (p *directiveParser) CanAcceptIndentedLine() bool { return false }

// allowedDirectives is the complete set of recognized directive names.
//
// The list is closed on purpose. Every directive is a block type the editor has
// to be able to produce and the renderer has to be able to display, so each
// addition is a commitment across the whole stack. Anything not here renders as
// plain text.
var allowedDirectives = map[string]bool{
	"video":   true,
	"file":    true,
	"embed":   true,
	"gallery": true,
}

// parseDirectiveHeader splits "video{src="..." poster="..."}" into its name and
// attributes.
func parseDirectiveHeader(s string) (name string, attributes map[string]string) {
	s = strings.TrimSpace(s)

	brace := strings.IndexByte(s, '{')
	if brace < 0 {
		// A directive with no attribute block is still a directive, though none
		// of the current three do anything useful without one.
		return strings.ToLower(strings.TrimSpace(s)), nil
	}

	name = strings.ToLower(strings.TrimSpace(s[:brace]))

	body := s[brace+1:]
	if end := strings.LastIndexByte(body, '}'); end >= 0 {
		body = body[:end]
	}

	return name, parseAttributes(body)
}

// parseAttributes reads a sequence of key="value" pairs.
//
// This is a small hand-rolled scanner rather than a regular expression because
// values legitimately contain spaces, equals signs, and slashes, and a regular
// expression that handles quoting correctly is harder to read than the loop
// below.
func parseAttributes(s string) map[string]string {
	attributes := make(map[string]string)

	i := 0
	for i < len(s) {
		// Skip whitespace between pairs.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}

		// Read the key, which runs up to the equals sign.
		keyStart := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(s[keyStart:i]))

		if i >= len(s) || s[i] != '=' {
			// A bare word with no value. Treated as a flag set to itself, which
			// keeps the parser from losing its place on malformed input.
			if key != "" {
				attributes[key] = key
			}
			continue
		}
		i++ // Step over the equals sign.

		if i >= len(s) {
			break
		}

		// Read the value, which may be quoted or bare.
		var value string
		if s[i] == '"' || s[i] == '\'' {
			quote := s[i]
			i++
			valueStart := i
			for i < len(s) && s[i] != quote {
				i++
			}
			value = s[valueStart:i]
			if i < len(s) {
				i++ // Step over the closing quote.
			}
		} else {
			valueStart := i
			for i < len(s) && s[i] != ' ' && s[i] != '\t' {
				i++
			}
			value = s[valueStart:i]
		}

		if key != "" {
			attributes[key] = value
		}
	}

	return attributes
}

// directiveExtension registers the parser with goldmark.
type directiveExtension struct{}

// Extend installs the block parser.
//
// The priority is chosen to run before the fenced code block parser, which
// would otherwise be a candidate for lines starting with a colon in some
// configurations. A lower number means higher precedence in goldmark.
func (e *directiveExtension) Extend(m goldmarkMarkdown) {
	m.Parser().AddOptions(
		parser.WithBlockParsers(
			util.PrioritizedValue{Value: &directiveParser{}, Priority: 100},
		),
	)
}
