package server

import (
	"strings"

	"git.thebytes.net/roberts/broadside/internal/config"
)

// Fonts are compiled into the binary and served from this origin.
//
// Linking to fonts.googleapis.com would have been one line, and it was rejected
// for three reasons. It would force the content security policy to allow an
// external origin for both styles and fonts, which weakens the policy for every
// page on the site. It would announce every reader's address to a third party
// on every visit, which is not a thing to do quietly on someone else's behalf.
// And it would make the site's appearance depend on a network Broadside does
// not control, so a homelab with no outbound access would render in Times New
// Roman.
//
// The files are open licensed, so redistributing them inside the binary is
// permitted. Only the Latin subset of each is embedded, which is what keeps the
// whole set to a few hundred kilobytes rather than several megabytes.

// FontOption is one entry in a font dropdown.
type FontOption struct {
	// Value is what gets written to config.json.
	Value string

	// Label is what the dropdown shows.
	Label string

	// Note is a short description, so somebody choosing a font has some idea
	// what they are picking without having to try all of them.
	Note string

	// Stack is the CSS family list, ending in a generic family so text still
	// renders sensibly in the moment before the file arrives.
	Stack string

	// BodySafe marks a face as suitable for long-form reading. See
	// ContentFontOptions for why the two dropdowns do not offer the same list.
	BodySafe bool
}

// fontOptions is every typeface compiled into the binary.
var fontOptions = []FontOption{
	{
		Value:    config.FontRaleway,
		Label:    "Raleway",
		Note:     "A clean humanist sans with an elegant lowercase.",
		Stack:    `Raleway, "Helvetica Neue", Arial, sans-serif`,
		BodySafe: true,
	},
	{
		Value:    config.FontNunito,
		Label:    "Nunito",
		Note:     "Rounded and friendly, with soft terminals.",
		Stack:    `Nunito, "Segoe UI", Roboto, sans-serif`,
		BodySafe: true,
	},
	{
		Value:    config.FontDomine,
		Label:    "Domine",
		Note:     "A sturdy serif drawn specifically for screen text.",
		Stack:    `Domine, Georgia, "Times New Roman", serif`,
		BodySafe: true,
	},
	{
		Value:    config.FontLiterata,
		Label:    "Literata",
		Note:     "Commissioned for Google Play Books. Built for reading at length.",
		Stack:    `Literata, Georgia, "Times New Roman", serif`,
		BodySafe: true,
	},
	{
		Value:    config.FontTypewriter,
		Label:    "Courier Prime",
		Note:     "A typewriter face, redrawn so it holds up at body size.",
		Stack:    `"Courier Prime", "Courier New", Courier, monospace`,
		BodySafe: true,
	},
	{
		Value: config.FontHandlee,
		Label: "Handlee",
		Note:  "A relaxed handwritten face. Titles only.",
		Stack: `Handlee, "Segoe Script", "Bradley Hand", cursive`,
		// Deliberately not body safe. A handwritten face is charming across a
		// title and exhausting across a thousand words, and offering it for
		// body text would mostly serve to let someone make their own site
		// unreadable.
		BodySafe: false,
	},
	{
		Value:    config.FontSystem,
		Label:    "System default",
		Note:     "Whatever the reader's device provides. No download at all.",
		Stack:    `-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`,
		BodySafe: true,
	},
}

// fontsByValue indexes the options for lookup.
var fontsByValue = func() map[string]FontOption {
	byValue := make(map[string]FontOption, len(fontOptions))
	for _, option := range fontOptions {
		byValue[option.Value] = option
	}
	return byValue
}()

// TitleFontOptions returns the choices for the title dropdown, which is every
// face. A title is short enough that any of them works.
func TitleFontOptions() []FontOption { return fontOptions }

// ContentFontOptions returns the choices for the body dropdown.
//
// This is a narrower list than the title one. The difference is not arbitrary
// tidiness: body text is read for minutes at a stretch, and a face that carries
// a five-word masthead beautifully can be genuinely tiring across a full post.
// Filtering here means the dropdown cannot be used to make the site unreadable
// by accident.
func ContentFontOptions() []FontOption {
	options := make([]FontOption, 0, len(fontOptions))
	for _, option := range fontOptions {
		if option.BodySafe {
			options = append(options, option)
		}
	}
	return options
}

// fontStackFor returns the CSS family list for a configured font name,
// resolving both built-in families and uploaded files.
//
// An unrecognized name falls back to the default rather than being passed
// through. Taking the value as text would let a hand-edited config inject
// arbitrary CSS into the stylesheet route, and it would not work anyway unless
// the reader happened to have that font installed.
func (s *Server) fontStackFor(name, fallback string) string {
	if strings.HasPrefix(name, config.UploadedFontPrefix) {
		file := strings.TrimPrefix(name, config.UploadedFontPrefix)

		// The file has to still exist. A font deleted while a page was open
		// would otherwise leave the site naming a family that no @font-face
		// rule defines, and every heading would silently fall back.
		for _, font := range s.listUploadedFonts() {
			if font.File == file {
				return quoteFamily(font.Family) + ", sans-serif"
			}
		}
		return fontsByValue[fallback].Stack
	}

	if option, found := fontsByValue[name]; found {
		return option.Stack
	}
	return fontsByValue[fallback].Stack
}
