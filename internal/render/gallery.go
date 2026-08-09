package render

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/util"
)

// This file renders the gallery directive, which is a set of images shown as a
// carousel:
//
//	:::gallery{caption="First light on the SV503"}
//	![Andromeda](/uploads/2026/08/08/01-m31.jpg "Two hours of exposure")
//	![Triangulum](/uploads/2026/08/08/02-m33.jpg)
//	:::
//
// The images are written on their own lines in ordinary markdown rather than
// packed into an attribute. That is the one form that stays readable and
// editable by hand once there are eight photographs in it, and it degrades
// honestly: strip the two directive lines and what remains is still a valid
// post containing the same images in the same order.
//
// What the server emits is only the strip and its slides. The arrows, the dots,
// and the wrap-around all appear at runtime, added by the site script, for the
// same reason the lightbox overlay lives in the layout template rather than
// being built in markup here: buttons in post output would mean teaching the
// sanitizer to allow buttons in post output, and the value of that allowlist is
// that it stays small. Without script the strip is a native horizontally
// scrolling row with scroll snapping, which swipes correctly on a phone and
// scrolls with shift-wheel or a trackpad on a desktop, and every slide is still
// a plain link to the full-size file.

// galleryImage is one slide.
type galleryImage struct {
	// Src is the image URL.
	Src string

	// Alt is the description read aloud, from the bracketed text.
	Alt string

	// Caption is the optional quoted title, shown in the lightbox for this
	// specific image rather than for the gallery as a whole.
	Caption string
}

// galleryImagePattern matches one markdown image on a line of its own.
//
// This is deliberately stricter than markdown's own image syntax: no leading
// text, no trailing text, and no reference-style links. A line inside a gallery
// that is not exactly one image is not something this can render, and skipping
// it is better than guessing.
var galleryImagePattern = regexp.MustCompile(`^\s*!\[([^\]]*)\]\(\s*(\S+?)(?:\s+"([^"]*)")?\s*\)\s*$`)

// parseGalleryImages reads the slides out of a directive's body.
func parseGalleryImages(d *Directive, source []byte) []galleryImage {
	var images []galleryImage

	for _, line := range d.Body(source) {
		if strings.TrimSpace(line) == "" {
			continue
		}

		match := galleryImagePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		// The URL is checked here rather than at render time so that a rejected
		// one does not leave a gap in the slide numbering.
		src := safeURL(match[2])
		if src == "" {
			continue
		}

		images = append(images, galleryImage{
			Src:     src,
			Alt:     match[1],
			Caption: match[3],
		})
	}

	return images
}

// renderGallery emits the carousel.
//
// A gallery that ends up with one usable image is rendered as an ordinary
// figure instead. A carousel holding a single slide has arrows that go nowhere
// and a dot strip with one dot, which looks broken rather than minimal.
func renderGallery(w util.BufWriter, d *Directive, source []byte) {
	images := parseGalleryImages(d, source)
	if len(images) == 0 {
		return
	}

	caption := html.EscapeString(d.Attr("caption"))

	if len(images) == 1 {
		renderSingleImage(w, images[0], caption)
		return
	}

	// The class is the hook the script looks for. A data attribute would read
	// better, but every one of those has to be named in the sanitizer's
	// allowlist, and class is already permitted, so this adds nothing to the
	// list of things a post is allowed to say.
	fmt.Fprintf(w, `<figure class="bs-gallery">`)
	fmt.Fprintf(w, `<div class="bs-gallery-track">`)

	for _, image := range images {
		slideCaption := image.Caption
		if slideCaption == "" {
			// Falling back to the gallery's own caption means the lightbox has
			// something to show for every slide rather than going blank on the
			// ones that were not captioned individually.
			slideCaption = d.Attr("caption")
		}

		fmt.Fprintf(w, `<a class="bs-gallery-slide" href=%q data-lightbox data-caption=%q>`,
			image.Src, html.EscapeString(slideCaption))
		fmt.Fprintf(w, `<img src=%q alt=%q loading="lazy" decoding="async">`,
			image.Src, html.EscapeString(image.Alt))
		fmt.Fprintf(w, `</a>`)
	}

	fmt.Fprintf(w, `</div>`)

	if caption != "" {
		fmt.Fprintf(w, `<figcaption>%s</figcaption>`, caption)
	}

	fmt.Fprintf(w, `</figure>`)
}

// renderSingleImage emits the same markup a standalone image produces, so a
// one-image gallery is indistinguishable from having written the image on its
// own line.
func renderSingleImage(w util.BufWriter, image galleryImage, galleryCaption string) {
	caption := html.EscapeString(image.Caption)
	if caption == "" {
		caption = galleryCaption
	}

	fmt.Fprintf(w, `<figure class="bs-image">`)
	fmt.Fprintf(w, `<a href=%q data-lightbox data-caption=%q>`, image.Src, caption)
	fmt.Fprintf(w, `<img src=%q alt=%q loading="lazy" decoding="async">`,
		image.Src, html.EscapeString(image.Alt))
	fmt.Fprintf(w, `</a>`)

	if caption != "" {
		fmt.Fprintf(w, `<figcaption>%s</figcaption>`, caption)
	}

	fmt.Fprintf(w, `</figure>`)
}
