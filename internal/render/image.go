package render

import (
	"fmt"
	"html"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// This file promotes standalone images to block-level figures wired for the
// lightbox.
//
// Markdown treats an image as inline, so "![alt](photo.jpg)" on its own line
// parses as a paragraph containing one image. Rendering the lightbox markup
// directly from the inline node would put a <figure> inside a <p>, which is
// invalid HTML: the browser closes the paragraph early to recover, and the
// resulting DOM does not match the CSS written against it. The fix is to spot
// the pattern during AST transformation and replace the whole paragraph with a
// block node.
//
// Images that genuinely sit inline within a sentence are left alone. They are
// rare in practice, and turning one into a full-width figure mid-paragraph
// would be wrong.

// KindImageFigure identifies the block node this file introduces.
var KindImageFigure = ast.NewNodeKind("ImageFigure")

// ImageFigure is a standalone image rendered as a block.
type ImageFigure struct {
	ast.BaseBlock

	// Destination is the image URL as written in the markdown.
	Destination string

	// AltText is the bracketed text, used for the alt attribute.
	AltText string

	// Title is the optional quoted string after the URL, which becomes the
	// visible caption.
	Title string
}

// Dump satisfies ast.Node for goldmark's debug output.
func (n *ImageFigure) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// Kind identifies the node type to the renderer.
func (n *ImageFigure) Kind() ast.NodeKind { return KindImageFigure }

// imageTransformer rewrites image-only paragraphs into ImageFigure nodes.
type imageTransformer struct{}

// Transform walks the document and performs the replacement.
func (t *imageTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()

	// Replacements are collected during the walk and applied afterward.
	// Mutating the tree while walking it invalidates the walker's position and
	// causes it to skip siblings.
	type replacement struct {
		parent    ast.Node
		paragraph ast.Node
		figure    *ImageFigure
	}
	var replacements []replacement

	ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		paragraph, ok := node.(*ast.Paragraph)
		if !ok {
			return ast.WalkContinue, nil
		}

		image := soleImage(paragraph)
		if image == nil {
			return ast.WalkContinue, nil
		}

		figure := &ImageFigure{
			Destination: string(image.Destination),
			AltText:     string(image.Text(source)),
			Title:       string(image.Title),
		}

		replacements = append(replacements, replacement{
			parent:    paragraph.Parent(),
			paragraph: paragraph,
			figure:    figure,
		})

		return ast.WalkSkipChildren, nil
	})

	for _, r := range replacements {
		if r.parent != nil {
			r.parent.ReplaceChild(r.parent, r.paragraph, r.figure)
		}
	}
}

// soleImage returns the image when a paragraph contains exactly one and nothing
// else of substance.
//
// Whitespace-only text nodes around the image are tolerated, because a trailing
// newline or a stray space is invisible to the author and should not change how
// the image renders.
func soleImage(paragraph *ast.Paragraph) *ast.Image {
	var found *ast.Image

	for child := paragraph.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *ast.Image:
			if found != nil {
				return nil // More than one image, so this is a gallery row rather than a standalone figure.
			}
			found = node

		case *ast.Text:
			// A soft or hard line break carries no visible content.
			if node.Segment.Len() == 0 || node.SoftLineBreak() || node.HardLineBreak() {
				continue
			}
			return nil // Real text alongside the image, so it is genuinely inline.

		default:
			return nil
		}
	}

	return found
}

// imageFigureRenderer emits the lightbox markup.
type imageFigureRenderer struct{}

// RegisterFuncs wires the renderer to the node type.
func (r *imageFigureRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindImageFigure, r.render)
}

// render writes a figure wrapping the image in a lightbox trigger.
//
// The anchor points at the image file itself, which is what makes this work
// with JavaScript disabled: clicking opens the full image as a plain navigation
// rather than doing nothing. The script upgrades that into an overlay, which is
// the same progressive enhancement approach the infinite scroll uses.
//
// loading="lazy" keeps images below the fold from being fetched until they are
// approached, which matters a great deal on a long timeline over a phone
// connection.
func (r *imageFigureRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	figure, ok := node.(*ImageFigure)
	if !ok {
		return ast.WalkContinue, nil
	}

	destination := safeURL(figure.Destination)
	if destination == "" {
		// An image whose URL was rejected renders as nothing rather than as a
		// broken element.
		return ast.WalkContinue, nil
	}

	alt := html.EscapeString(figure.AltText)
	caption := html.EscapeString(figure.Title)

	fmt.Fprintf(w, `<figure class="bs-image">`)

	// data-lightbox is the hook the script looks for. The caption is carried in
	// a data attribute so the overlay can show it without reaching back into
	// the DOM for the figcaption, which may not exist.
	fmt.Fprintf(w, `<a href=%q data-lightbox data-caption=%q>`, destination, caption)
	fmt.Fprintf(w, `<img src=%q alt=%q loading="lazy" decoding="async">`, destination, alt)
	fmt.Fprintf(w, `</a>`)

	if caption != "" {
		fmt.Fprintf(w, `<figcaption>%s</figcaption>`, caption)
	}

	fmt.Fprintf(w, `</figure>`)

	return ast.WalkContinue, nil
}
