// Package config holds the site's settings, stored as core/config.json.
//
// Everything an operator can adjust lives in this one file, including the
// theme. An earlier design put colors in a separate core/themes/active.json,
// which meant two files to mount into a container, two files to back up, and
// two places to look when something rendered wrong. Since the theme is a short
// list of values rather than a template system, folding it into the config
// removes a moving part without giving anything up.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"git.thebytes.net/roberts/broadside/internal/safepath"
)

// Path is where the config lives, relative to the site root.
const Path = "core/config.json"

// Config is the complete set of site settings.
//
// Every field has a working default, so a site folder with no config file at
// all still starts and serves. That matters for the container image, where the
// first run happens before anyone has had a chance to write anything.
type Config struct {
	// SetupComplete gates the whole site until an account exists.
	//
	// Until this is true, every request is answered with the setup page. That
	// is stricter than leaving the site readable and only guarding the admin,
	// and it is the right default: between first boot and somebody claiming the
	// site, anyone who finds it can claim it. A container port-forwarded before
	// its owner gets around to configuring it is a real situation, and this
	// closes that window rather than documenting it.
	SetupComplete bool `json:"setup_complete"`

	// Title appears in the header, the browser tab, and the feed.
	Title string `json:"title"`

	// Slogan is the line beside the title in the header, and the feed's
	// description.
	Slogan string `json:"slogan"`

	// DisplayName is the author's name as readers see it, in the footer and in
	// the feed.
	//
	// It is separate from the login username so that changing how you are
	// credited is not the same act as changing how you sign in. Publishing your
	// login name on every page is also a small gift to anyone attacking the
	// login form, since it hands them half the credential.
	DisplayName string `json:"display_name"`

	// BaseURL is the site's public address, used to build absolute links in the
	// feed and the sitemap. Feed readers need absolute URLs and the server
	// cannot reliably infer the public address from behind a proxy, so this has
	// to be stated rather than guessed.
	BaseURL string `json:"base_url"`

	// Image is the picture in the header, as a site path.
	Image string `json:"image"`

	// Favicon is the browser tab icon, as a site path.
	Favicon string `json:"favicon"`

	// FooterText is the line above the copyright notice. Empty uses the
	// default.
	FooterText string `json:"footer_text"`

	// DateFormat controls how a post's date is written. See dateformat.go for
	// the tokens.
	DateFormat string `json:"date_format"`

	// Social lists the profile links shown under the header.
	Social []SocialLink `json:"social"`

	// Timezone names the location used to interpret timestamps that carry no
	// offset, and to decide which day a post belongs to. Stored as an IANA name
	// such as "America/Chicago" rather than a fixed offset, so daylight saving
	// is handled correctly.
	Timezone string `json:"timezone"`

	// PostsPerPage is how many posts a timeline request returns.
	PostsPerPage int `json:"posts_per_page"`

	// MaxUploadMB caps a single uploaded file, in megabytes.
	//
	// The old fixed limit of fifty was chosen for a phone photograph and is far
	// too small for the work people actually want to publish: a stacked
	// astrophotography frame or a scanned negative runs to hundreds of
	// megabytes before anything unusual is going on.
	//
	// A limit still exists because without one a single request can fill the
	// disk of the small machine this is designed to run on, and because the
	// error you get from a full disk is much worse than the error you get from
	// a refused upload.
	MaxUploadMB int `json:"max_upload_mb"`

	// MinPasswordLength is the floor for a new password.
	//
	// Length is the property that actually resists an offline attack against a
	// stolen hash, so this is the setting that matters rather than any rule
	// about punctuation or mixed case. It is adjustable because the right
	// answer differs between a blog on a home network and one on the open
	// internet, and because a floor somebody finds unreasonable is a floor they
	// work around.
	MinPasswordLength int `json:"min_password_length"`

	// Language is the value for the html lang attribute and the feed.
	Language string `json:"language"`

	// Theme is the appearance: six colors and three typefaces.
	Theme Theme `json:"theme"`
}

// DefaultFooterText is used when the operator has not written their own.
const DefaultFooterText = "Published with Broadside."

// Upload and password bounds.
const (
	// MaxUploadDefaultMB is the starting limit for a new site.
	MaxUploadDefaultMB = 256

	// MaxUploadCeilingMB is as high as the setting goes.
	//
	// Four gigabytes is past anything a blog post reasonably carries and is
	// where the practical limits start: a browser holds a multipart upload
	// together for the whole transfer, and a request that takes long enough
	// will meet a proxy timeout somewhere before it meets this.
	MaxUploadCeilingMB = 4096

	// MinPasswordDefault is the floor for a new password.
	//
	// Eight is the common baseline and is what most people expect. Raising it
	// is a setting rather than a rule here, because the right answer differs
	// between a blog on a home network and one facing the internet.
	MinPasswordDefault = 8

	// MinPasswordFloor is the shortest the setting itself may be set to.
	//
	// Below this the hashing stops being the thing protecting the account: six
	// characters is inside the range an attacker with a stolen hash can simply
	// enumerate, however good the algorithm is.
	MinPasswordFloor = 6
)

// SocialLink is one profile shown in the header.
type SocialLink struct {
	// Platform selects the icon. The recognized names live in the server's
	// generated platform table, plus "custom" for an uploaded icon.
	Platform string `json:"platform"`

	// URL is either a full web address or a bare username. A username is
	// expanded through the platform's profile template, because being made to
	// look up the URL format for nine different services is exactly the kind of
	// friction that stops a setting from being used.
	URL string `json:"url"`

	// Label overrides the accessible name, and is what a custom entry is
	// called.
	Label string `json:"label,omitempty"`

	// Icon is the uploaded image for a custom entry, as a site path. It is
	// ignored for a known platform, which draws its own mark.
	Icon string `json:"icon,omitempty"`
}

// CustomPlatform is the platform value for a link with an uploaded icon.
const CustomPlatform = "custom"

// Theme is the site's appearance.
//
// There is no template language and no layout engine, because the moment a
// theme can change structure it becomes a theme system, and a theme system
// needs versioning, compatibility rules, and somewhere to distribute them from.
//
// Values are emitted as CSS custom properties on :root, which is what lets the
// stylesheet reference them without any of it being compiled in advance.
type Theme struct {
	// Background is the page background.
	Background string `json:"background"`

	// Surface is the background of raised elements such as code blocks and
	// inputs. It should sit close to Background rather than contrast with it.
	Surface string `json:"surface"`

	// Text is the main body color.
	Text string `json:"text"`

	// Muted is for dates, captions, and other secondary text. It has to stay
	// legible, so it should not drift too far toward the background.
	Muted string `json:"muted"`

	// Accent is links and interactive elements.
	Accent string `json:"accent"`

	// Border is rules, dividers, and outlines.
	Border string `json:"border"`

	// The three typefaces.
	//
	// Titles are split from body text because they do different jobs: a title
	// is seen once and can afford some character, whereas body text is read for
	// minutes at a stretch and should get out of the way. The site title is
	// split from post titles for the same reason one step further down, since a
	// masthead can carry a face that would be too much repeated above every
	// post on the page.
	SiteTitleFont string `json:"site_title_font"`
	PostTitleFont string `json:"post_title_font"`
	ContentFont   string `json:"content_font"`
}

// LightTheme is the default palette: paper and pencil lead.
//
// The background is a warm off-white rather than pure white, because #ffffff on
// a backlit screen is glare rather than paper, and the warmth is what makes a
// long page of text comfortable to sit with. The text is graphite rather than
// black for the same reason: true black on white is a harsher contrast than
// print ever produces.
//
// The accent is a muted ink blue. A saturated link color would be the loudest
// thing on a page whose entire premise is that the writing comes first.
var LightTheme = Theme{
	Background: "#fbfaf7",
	Surface:    "#f1efe9",
	Text:       "#2b2a27",
	Muted:      "#6f6c64",
	Accent:     "#3d5a80",
	Border:     "#e2ded4",

	SiteTitleFont: FontNunito,
	PostTitleFont: FontNunito,
	ContentFont:   FontRaleway,
}

// DarkTheme is the alternative palette, for operators who prefer a dark page.
var DarkTheme = Theme{
	Background: "#141412",
	Surface:    "#1d1d1a",
	Text:       "#e6e3db",
	Muted:      "#918d83",
	Accent:     "#8fb4d9",
	Border:     "#2e2e2a",

	SiteTitleFont: FontNunito,
	PostTitleFont: FontNunito,
	ContentFont:   FontRaleway,
}

// The recognized values for the built-in typefaces.
//
// Each names a family compiled into the binary. The server holds the full
// registry, including the CSS stacks and the labels the settings dropdowns
// show; these constants exist so the defaults above can be written in terms of
// names rather than magic strings.
//
// An uploaded font is referenced by UploadedFontPrefix followed by its
// filename, which is how the server tells the two apart.
const (
	// FontRaleway is a humanist sans, and the body default.
	FontRaleway = "raleway"

	// FontNunito is a rounded sans, and the title default.
	FontNunito = "nunito"

	// FontDomine is a serif drawn for screen text.
	FontDomine = "domine"

	// FontLiterata was commissioned for Google Play Books and is built for
	// reading at length.
	FontLiterata = "literata"

	// FontTypewriter is Courier Prime, a Courier redrawn for screens.
	FontTypewriter = "courier-prime"

	// FontHandlee is a relaxed handwritten face, offered for titles only. A
	// whole post set in it would be unreadable.
	FontHandlee = "handlee"

	// FontSystem uses whatever the reader's device provides, at no download.
	FontSystem = "system"
)

// UploadedFontPrefix marks a font value as referring to an uploaded file rather
// than a built-in family.
const UploadedFontPrefix = "upload:"

// Default returns a config that produces a working site with no setup.
func Default() Config {
	return Config{
		SetupComplete:     false,
		Title:             "Broadside",
		Slogan:            "A blog.",
		Timezone:          "UTC",
		PostsPerPage:      20,
		MaxUploadMB:       MaxUploadDefaultMB,
		MinPasswordLength: MinPasswordDefault,
		Language:          "en",
		DateFormat:        DefaultDateFormat,
		FooterText:        DefaultFooterText,
		Theme:             LightTheme,
	}
}

// Load reads the config from the site root, filling in defaults for anything
// absent.
//
// A missing file is not an error. It means a fresh site, which should start and
// present its setup page rather than refuse to run over a file nobody has
// written yet.
func Load(root *safepath.Root) (Config, error) {
	cfg := Default()

	data, err := root.ReadFile(Path)
	if err != nil {
		// Any read failure, including a missing file, falls back to defaults.
		// Distinguishing absent from unreadable would mean refusing to start
		// over a permissions problem the operator can see in the logs anyway.
		return cfg, nil
	}

	// Decoding into the already-populated default means any key the file omits
	// keeps its default rather than becoming a zero value. Without this, a
	// config specifying only a title would silently set PostsPerPage to zero
	// and return an empty timeline.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("config: parsing %s: %w", Path, err)
	}

	cfg.applyFallbacks()
	return cfg, nil
}

// saveMu serializes writes to the config file.
//
// There is more than one way to save settings now: the admin form, and the
// settings API that a phone or an automation calls. Two of those arriving at
// once used to interleave inside one file, and the result was not a lost update
// but a corrupt document, because the write below truncates before it fills.
var saveMu sync.Mutex

// Save writes the config back to the site root.
//
// Written to a temporary file and renamed over the target, for the same reason
// posts are: a config file is the whole of a site's settings, and the failure
// mode of writing it in place is that a crash, a full disk, or a second writer
// arriving mid-write leaves an empty or half-written file where the settings
// used to be. A rename is atomic, so a reader sees either the old file or the
// new one and never a mixture.
//
// The fsync before the rename is what makes this survive a power loss rather
// than merely a crash. Without it the rename can reach disk before the contents
// do, which leaves a config that exists and is empty.
func Save(root *safepath.Root, cfg Config) error {
	cfg.applyFallbacks()

	// Indented output because this file is meant to be opened and edited by
	// hand, which is the same reason the content is markdown rather than a
	// database row.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	data = append(data, '\n')

	saveMu.Lock()
	defer saveMu.Unlock()

	// The temporary name carries the process id and a nanosecond timestamp so
	// that two Broadside instances pointed at the same folder, which is not
	// supported but does happen, cannot collide on it.
	tempPath := fmt.Sprintf("%s.tmp-%d-%d", Path, os.Getpid(), time.Now().UnixNano())

	f, err := root.Create(tempPath)
	if err != nil {
		return fmt.Errorf("config: creating temporary file: %w", err)
	}

	// Any failure from here on removes the temporary file, or a failing disk
	// slowly litters the site folder with debris.
	cleanup := func() { root.Remove(tempPath) }

	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("config: writing temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("config: flushing temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: closing temporary file: %w", err)
	}

	if err := root.Rename(tempPath, Path); err != nil {
		cleanup()
		return fmt.Errorf("config: replacing %s: %w", Path, err)
	}
	return nil
}

// applyFallbacks repairs values that would otherwise produce a broken site.
//
// A hand-edited config is expected, and a typo in it should degrade one setting
// rather than take the site down. Each repair below turns a value that cannot
// work into the default that can.
func (c *Config) applyFallbacks() {
	if strings.TrimSpace(c.Title) == "" {
		c.Title = "Broadside"
	}
	if c.PostsPerPage <= 0 {
		c.PostsPerPage = 20
	}
	if c.PostsPerPage > 200 {
		// An unbounded page size turns one request into a full site render,
		// which is a cheap way for a crawler to make the server unhappy.
		c.PostsPerPage = 200
	}
	if c.MaxUploadMB <= 0 {
		c.MaxUploadMB = MaxUploadDefaultMB
	}
	if c.MaxUploadMB > MaxUploadCeilingMB {
		c.MaxUploadMB = MaxUploadCeilingMB
	}
	if c.MinPasswordLength < MinPasswordFloor {
		c.MinPasswordLength = MinPasswordDefault
	}
	if strings.TrimSpace(c.Language) == "" {
		c.Language = "en"
	}
	if strings.TrimSpace(c.Timezone) == "" {
		c.Timezone = "UTC"
	}
	if !ValidDateFormat(c.DateFormat) {
		c.DateFormat = DefaultDateFormat
	}
	if strings.TrimSpace(c.FooterText) == "" {
		c.FooterText = DefaultFooterText
	}

	// A trailing slash on the base URL produces "https://example.com//2026/..."
	// in the feed, which some readers treat as a different URL and re-announce
	// as a new post.
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")

	c.Theme.applyFallbacks()
}

// applyFallbacks fills in any theme value left blank, so a partial theme still
// renders. Someone changing only the accent color should not have to restate
// the other five.
func (t *Theme) applyFallbacks() {
	if t.Background == "" {
		t.Background = LightTheme.Background
	}
	if t.Surface == "" {
		t.Surface = LightTheme.Surface
	}
	if t.Text == "" {
		t.Text = LightTheme.Text
	}
	if t.Muted == "" {
		t.Muted = LightTheme.Muted
	}
	if t.Accent == "" {
		t.Accent = LightTheme.Accent
	}
	if t.Border == "" {
		t.Border = LightTheme.Border
	}
	if t.SiteTitleFont == "" {
		t.SiteTitleFont = LightTheme.SiteTitleFont
	}
	if t.PostTitleFont == "" {
		t.PostTitleFont = LightTheme.PostTitleFont
	}
	if t.ContentFont == "" {
		t.ContentFont = LightTheme.ContentFont
	}
}

// Location resolves the configured timezone.
//
// An unrecognized name falls back to UTC rather than failing, because the
// timezone affects which day a post is filed under and nothing more. A site
// running an hour off is a small problem; a site that will not start is a
// large one.
func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// AbsoluteURL turns a site-relative path into a full URL using BaseURL.
//
// When BaseURL is unset the path is returned unchanged, producing a feed with
// relative links: technically incomplete, but better than emitting a URL built
// from a guessed hostname that points somewhere wrong.
func (c Config) AbsoluteURL(path string) string {
	if c.BaseURL == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.BaseURL + path
}

// FormatDate renders a time using the site's configured format.
func (c Config) FormatDate(t time.Time) string {
	return FormatDate(t, c.DateFormat)
}

// MaxUploadBytes returns the upload limit in bytes.
func (c Config) MaxUploadBytes() int64 {
	return int64(c.MaxUploadMB) << 20
}
