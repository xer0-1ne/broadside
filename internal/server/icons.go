package server

import (
	"html/template"
	"strings"

	"git.thebytes.net/roberts/broadside/internal/config"
)

// Social icons are inline SVG rather than an icon font or an image set.
//
// A font needs a file, a second request, and a fallback for the moment before
// it loads, during which the header shows replacement glyphs. Images need a
// request each. Inlining costs a few hundred bytes per icon in the HTML,
// renders with the page, inherits the theme color through currentColor, and
// stays crisp at any size. For a handful of icons that is the right trade.
//
// The path data lives in platforms_gen.go. See that file for where the marks
// come from and which ones are drawn by hand.

// platformsByValue indexes the generated table for lookup.
//
// Built once at startup rather than searched linearly per render, since the
// header draws every configured icon on every page.
var platformsByValue = func() map[string]Platform {
	byValue := make(map[string]Platform, len(platforms))
	for _, p := range platforms {
		byValue[p.Value] = p
	}
	return byValue
}()

// Platforms returns every known service, ordered by label.
//
// This is what populates the platform dropdown in the settings UI, so the list
// the operator picks from and the list the renderer knows how to draw are the
// same list by construction. There is no second table to keep in step.
func Platforms() []Platform { return platforms }

// resolveSocial turns a configured link into something renderable.
//
// The operator may give either a full URL or a bare handle, because being made
// to look up the profile URL format for nine different services is exactly the
// kind of small friction that stops a setting from being used. A value that
// looks like an address is taken as one; anything else is treated as a handle
// and expanded through the platform's template.
//
// Services with no derivable profile address, such as Discord and Mastodon,
// return an empty URL when given only a handle. Mastodon is federated so a
// handle without its instance is not addressable, and Discord has no public
// profile page keyed on a username. Those render as a plain icon with the
// handle as its label rather than a broken link, which is more useful than
// guessing an address that leads nowhere.
func resolveSocial(link config.SocialLink) (url, name string, known bool) {
	key := strings.ToLower(strings.TrimSpace(link.Platform))

	// A custom entry carries its own label and uploaded icon, so it is not in
	// the generated platform table and is handled before the lookup.
	if key == config.CustomPlatform {
		value := strings.TrimSpace(link.URL)
		label := strings.TrimSpace(link.Label)
		if value == "" || label == "" || strings.TrimSpace(link.Icon) == "" {
			return "", "", false
		}
		if looksLikeURL(value) {
			return value, label, true
		}
		// A custom service has no profile template to expand a bare handle
		// through, so the handle becomes the label and the icon does not link.
		return "", label + ": " + strings.TrimPrefix(value, "@"), true
	}

	platform, known := platformsByValue[key]
	if !known {
		return "", "", false
	}

	value := strings.TrimSpace(link.URL)
	if value == "" {
		return "", "", false
	}

	name = link.Label
	if name == "" {
		name = platform.Label
	}

	if looksLikeURL(value) {
		return value, name, true
	}

	// A handle. Leading decorations people habitually type are stripped, since
	// "@robert" and "robert" mean the same account and the template supplies
	// whatever prefix the service actually needs.
	handle := strings.TrimPrefix(value, "@")
	handle = strings.TrimPrefix(handle, "/")

	if platform.HandleURL == "" {
		// No derivable address. The handle still identifies the account, so it
		// becomes the label and the icon renders without a link.
		return "", name + ": " + handle, true
	}

	return strings.ReplaceAll(platform.HandleURL, "{h}", handle), name, true
}

// looksLikeURL reports whether a configured value is already an address.
//
// The test is on the scheme rather than on the presence of a dot, because
// plenty of handles contain dots. A protocol-relative "//example.com" counts
// too, since that is unambiguously an address.
func looksLikeURL(value string) bool {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"http://", "https://", "mailto:", "//"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// socialIcon renders the mark for a platform.
//
// The output is marked safe because it is assembled from the generated table
// and contains no operator-supplied text. The platform name selects a path but
// is never interpolated into the markup, so a config entry cannot inject
// anything.
func socialIcon(platform string) template.HTML {
	entry, found := platformsByValue[strings.ToLower(strings.TrimSpace(platform))]
	if !found {
		return ""
	}

	// aria-hidden because the accessible name lives on the surrounding anchor.
	// Without it a screen reader announces the icon separately, which is noise.
	var b strings.Builder
	b.WriteString(`<svg class="social-icon" viewBox="0 0 24 24" width="18" height="18" `)
	b.WriteString(`fill="currentColor" aria-hidden="true" focusable="false"><path d="`)
	b.WriteString(entry.Path)
	b.WriteString(`"/></svg>`)

	return template.HTML(b.String())
}
