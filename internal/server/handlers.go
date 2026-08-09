package server

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/index"
)

// pageData is the value every template receives.
//
// One struct for all pages means the layout can rely on the site fields being
// populated no matter which handler rendered the page, without each handler
// having to remember to fill them in.
type pageData struct {
	// Site-wide values, needed by the layout on every page.
	Title        string
	Slogan       string
	Author       string
	Language     string
	Image        string
	Favicon      string
	FooterText   string
	CanonicalURL string
	Social       []socialView

	// LoggedIn drives the account controls in the nav.
	LoggedIn bool

	// ShowSearch draws the search rule.
	//
	// Only the timeline sets it. Search filters that one page in place, so on a
	// permalink, the admin, or the sign-in form there is nothing for it to
	// filter and the control would either do nothing or navigate the reader
	// away from what they were doing. It is off by default so a page has to ask
	// for it rather than inherit it by being rendered through the layout.
	ShowSearch bool

	// Admin carries the admin-only fields. It is nil on every public page, so a
	// template that reaches for it outside the admin fails loudly during
	// development rather than rendering a blank.
	Admin *adminData

	// AssetVersion busts the cache on the stylesheet and scripts when they
	// change. See assetversion.go for why the alternative is a site that looks
	// broken for an hour after every upgrade.
	AssetVersion string

	// Login page state.
	Error    string
	Next     string
	FirstRun bool

	// MinPasswordLength drives the browser-side length check on the setup and
	// sign-in forms, so the rule the server enforces is the one the field
	// advertises rather than a number written separately in the markup.
	MinPasswordLength int

	// Heading and Subheading are used by the error page. Ordinary pages leave
	// them empty, since the site title in the header is the only heading a
	// minimal layout needs.
	Heading    string
	Subheading string

	Posts []postView
	Post  *postView

	// Search state, echoed back so the field keeps its value and the mode
	// buttons show which one is active.
	Query       string
	SearchMode  string
	ResultCount int
	ResultNoun  string

	// NextURL is the address of the following page, empty on the last one. The
	// template renders it as a real link, which infinite scroll then hijacks.
	NextURL string
}

// socialView is one header link prepared for display.
type socialView struct {
	URL  string
	Name string
	Icon template.HTML

	// Custom is the path to an uploaded icon, set only for a custom entry.
	// When present the template renders an img rather than the inline mark.
	Custom string
}

// postView is a post prepared for display.
//
// Dates arrive pre-formatted so that formatting lives in Go, where it can be
// tested, rather than spread across template expressions.
type postView struct {
	index.PostMeta

	// BodyHTML is the rendered, sanitized markdown. The timeline shows posts in
	// full, so this is populated for every post on the page rather than only on
	// a permalink.
	BodyHTML string

	DisplayDate string
	MachineDate string
}

// handleTimeline renders the front page, and doubles as the search results
// view.
//
// Search deliberately has no page of its own. Filtering happens here and the
// results replace the post list, so a reader never leaves the timeline and
// never loses the layout they were reading in. The URL still carries the query,
// which keeps results shareable and the back button honest.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := parseSearchMode(r)

	data := s.newPageData(r)
	data.ShowSearch = true
	data.Query = query
	data.SearchMode = string(mode)

	if query != "" {
		results := s.search(query, mode, s.filter(r))

		data.Posts = s.viewsFor(results)
		data.ResultCount = len(results)
		data.ResultNoun = plural(len(results), "result", "results")

		// Results are not paginated. A query narrow enough to be useful returns
		// few enough posts to read, and a cursor over a filtered set would have
		// to re-run the search for every page.
		s.renderTimeline(w, r, data)
		return
	}

	cursor := parseCursor(r)
	page := s.index.Query(s.filter(r), cursor, s.cfg.PostsPerPage)
	data.Posts = s.viewsFor(page.Posts)

	if page.HasMore {
		data.NextURL = "/?before=" + url.QueryEscape(encodeCursor(page.Next))
	}

	s.renderTimeline(w, r, data)
}

// renderTimeline sends either the full page or the bare post list, depending on
// who asked.
func (s *Server) renderTimeline(w http.ResponseWriter, r *http.Request, data pageData) {
	if isFragmentRequest(r) {
		s.renderPage(w, r, "timeline-page.html", data)
		return
	}
	s.renderPage(w, r, "timeline.html", data)
}

// handlePost renders one post at its permalink.
//
// This is registered as the catch-all, so it receives every request no more
// specific route claimed. The path is parsed here rather than by a wildcard
// pattern; see the routing table for why.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	// A canonical post URL is exactly "/YYYY/MM/DD/slug". Anything shaped
	// differently was never a post address.
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) != 4 {
		s.renderNotFound(w, r)
		return
	}

	year, err1 := strconv.Atoi(segments[0])
	month, err2 := strconv.Atoi(segments[1])
	day, err3 := strconv.Atoi(segments[2])
	slug := segments[3]

	if err1 != nil || err2 != nil || err3 != nil || month < 1 || month > 12 {
		s.renderNotFound(w, r)
		return
	}

	meta, found := s.index.Lookup(year, time.Month(month), day, slug)
	if !found {
		s.renderNotFound(w, r)
		return
	}

	// A draft or scheduled post must not be reachable by guessing its URL,
	// which the index lookup alone does not prevent since it searches
	// everything.
	if !s.filter(r).Matches(meta, time.Now().In(s.cfg.Location())) {
		s.renderNotFound(w, r)
		return
	}

	view, err := s.viewFor(meta)
	if err != nil {
		s.logger.Error("reading post", "path", meta.Path, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "That post could not be read.")
		return
	}

	data := s.newPageData(r)
	data.Heading = meta.Title
	data.Post = &view
	data.Posts = []postView{view}
	data.CanonicalURL = s.cfg.AbsoluteURL(meta.URL)

	s.renderPage(w, r, "post.html", data)
}

// handleUpload serves a file from the uploads directory.
//
// This is a handler rather than an http.FileServer because the response headers
// have to be set deliberately. A file server infers the content type from the
// extension and serves everything inline, which means an uploaded HTML file
// would render as a page on the site's own origin: a stored cross-site
// scripting vector rather than a theoretical one.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// PathValue returns the decoded path, so "%2e%2e" arrives here as "..".
	//
	// The read goes through a root confined to the uploads directory rather
	// than through the site root. That distinction is load bearing. An earlier
	// version joined this segment onto "uploads/" and read through the site
	// root, which meant path.Join resolved "../core/config.json" to
	// "core/config.json" before any check ran: a clean path, inside the site
	// root, pointing at the config file. The site root was guarding the wrong
	// boundary. Confining to uploads makes the same request fail, because ".."
	// genuinely leads above this root.
	requested := r.PathValue("path")

	data, err := s.uploads.ReadFile(requested)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.renderNotFound(w, r)
			return
		}
		s.renderNotFound(w, r)
		return
	}

	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(requested)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Only images, video, and audio are served inline. Everything else is sent
	// as an attachment so the browser downloads it rather than rendering it on
	// this origin. That single header is what stops an uploaded SVG or HTML
	// file from executing script in the site's context.
	inline := strings.HasPrefix(contentType, "image/") ||
		strings.HasPrefix(contentType, "video/") ||
		strings.HasPrefix(contentType, "audio/")

	// SVG is the exception among images. It is XML that can carry script, so it
	// is never served inline even though its type says image.
	if strings.Contains(contentType, "svg") {
		inline = false
		contentType = "application/octet-stream"
	}

	disposition := "attachment"
	if inline {
		disposition = "inline"
	}

	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", disposition+"; filename="+strconv.Quote(path.Base(requested)))
	header.Set("X-Content-Type-Options", "nosniff")

	// Uploads are immutable: the naming scheme gives every file a unique path,
	// so a changed image is a new URL. That makes a long cache lifetime safe and
	// keeps a timeline full of photographs from refetching them on every visit.
	header.Set("Cache-Control", "public, max-age=31536000, immutable")

	http.ServeContent(w, r, path.Base(requested), time.Time{}, strings.NewReader(string(data)))
}

// handleThemeCSS emits the configured colors and font as custom properties.
//
// Serving this as a stylesheet rather than an inline style block is what allows
// the content security policy to forbid inline styles entirely. The cost is one
// extra request, which is cached.
func (s *Server) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	theme := s.cfg.Theme

	// Colors go through a validator rather than being interpolated directly.
	// The values come from a config file the operator edits by hand, and a
	// stray closing brace would otherwise let arbitrary CSS into every page.
	var b strings.Builder
	b.WriteString(":root{")
	fmt.Fprintf(&b, "--bs-bg:%s;", safeColor(theme.Background, config.LightTheme.Background))
	fmt.Fprintf(&b, "--bs-surface:%s;", safeColor(theme.Surface, config.LightTheme.Surface))
	fmt.Fprintf(&b, "--bs-text:%s;", safeColor(theme.Text, config.LightTheme.Text))
	fmt.Fprintf(&b, "--bs-muted:%s;", safeColor(theme.Muted, config.LightTheme.Muted))
	fmt.Fprintf(&b, "--bs-accent:%s;", safeColor(theme.Accent, config.LightTheme.Accent))
	fmt.Fprintf(&b, "--bs-border:%s;", safeColor(theme.Border, config.LightTheme.Border))

	// Font stacks are chosen from a fixed table rather than taken from the
	// config as text, so nothing operator-supplied reaches the stylesheet.
	fmt.Fprintf(&b, "--bs-font-site-title:%s;", s.fontStackFor(theme.SiteTitleFont, config.LightTheme.SiteTitleFont))
	fmt.Fprintf(&b, "--bs-font-post-title:%s;", s.fontStackFor(theme.PostTitleFont, config.LightTheme.PostTitleFont))
	fmt.Fprintf(&b, "--bs-font-body:%s;", s.fontStackFor(theme.ContentFont, config.LightTheme.ContentFont))
	b.WriteString("}")

	// Uploaded typefaces need their own @font-face rules, which cannot live in
	// the compiled stylesheet because the files did not exist when it was
	// built. Emitting them here keeps them under the same content security
	// policy as everything else, since they are served from this origin.
	for _, font := range s.listUploadedFonts() {
		fmt.Fprintf(&b, "@font-face{font-family:%s;font-display:swap;src:url(\"/fonts/%s\")}",
			quoteFamily(font.Family), font.File)
	}

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	// Short cache, because a theme change should show up quickly and the file is
	// a few hundred bytes.
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Write([]byte(b.String()))
}

// safeColor validates a CSS color value, falling back when it is not one.
//
// This is an allowlist rather than an escape, because CSS has enough syntax
// that escaping correctly is harder than refusing anything unexpected.
func safeColor(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	// Hex form: three, four, six, or eight digits after the hash.
	if strings.HasPrefix(value, "#") {
		digits := value[1:]
		switch len(digits) {
		case 3, 4, 6, 8:
			for _, r := range digits {
				isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
				if !isHex {
					return fallback
				}
			}
			return value
		}
		return fallback
	}

	// Functional notation such as rgb(), hsl(), and oklch(). The contents are
	// restricted to characters that cannot terminate a declaration or open a
	// new one.
	for _, prefix := range []string{"rgb(", "rgba(", "hsl(", "hsla(", "oklch(", "lab("} {
		if strings.HasPrefix(strings.ToLower(value), prefix) && strings.HasSuffix(value, ")") {
			for _, r := range value {
				switch {
				case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
				case r == '(', r == ')', r == ',', r == '.', r == '%', r == ' ', r == '/', r == '-':
				default:
					return fallback
				}
			}
			return value
		}
	}

	// A bare keyword such as "black" or "transparent". Letters only, so it
	// cannot carry syntax.
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') {
			return fallback
		}
	}
	return value
}

// handleRobots emits a permissive robots file pointing at the sitemap.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	body := "User-agent: *\nAllow: /\n"
	if s.cfg.BaseURL != "" {
		body += "\nSitemap: " + s.cfg.AbsoluteURL("/sitemap.xml") + "\n"
	}
	w.Write([]byte(body))
}

// handleHealth reports liveness and index statistics.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"index":  s.index.Stats(),
	})
}

// newPageData builds the values every template expects.
func (s *Server) newPageData(r *http.Request) pageData {
	// Every page asks, including the public ones, because the author's bar
	// appears on all of them. This is one map lookup against the session store,
	// which is cheap enough to do per request and far simpler than threading a
	// flag through every handler.
	_, signedIn := s.currentSession(r)

	data := pageData{
		AssetVersion:      assetVersion,
		MinPasswordLength: s.cfg.MinPasswordLength,
		LoggedIn:          signedIn,
		Title:             s.cfg.Title,
		Slogan:            s.cfg.Slogan,
		Author:            s.cfg.DisplayName,
		Language:          s.cfg.Language,
		Image:             s.cfg.Image,
		Favicon:           s.cfg.Favicon,
		FooterText:        s.cfg.FooterText,
		CanonicalURL:      s.cfg.AbsoluteURL(r.URL.Path),
		SearchMode:        string(SearchContent),
	}

	for _, link := range s.cfg.Social {
		url, name, known := resolveSocial(link)
		if !known {
			// An unrecognized platform or an empty value is skipped rather than
			// rendered as a blank space in the header.
			continue
		}
		data.Social = append(data.Social, socialView{
			URL:    url,
			Name:   name,
			Icon:   socialIcon(link.Platform),
			Custom: link.Icon,
		})
	}

	return data
}

// viewsFor prepares a list of posts for display.
func (s *Server) viewsFor(posts []index.PostMeta) []postView {
	views := make([]postView, 0, len(posts))
	for _, post := range posts {
		view, err := s.viewFor(post)
		if err != nil {
			// A post that cannot be read is skipped rather than breaking the
			// whole page. The error is logged so the operator can find it.
			s.logger.Error("preparing post for display", "path", post.Path, "error", err)
			continue
		}
		views = append(views, view)
	}
	return views
}

// viewFor prepares a single post, body included.
//
// Every post on the page is rendered in full, which is why the render cache
// exists. On a warm cache this touches the disk once for a stat and does no
// markdown parsing at all.
func (s *Server) viewFor(meta index.PostMeta) (postView, error) {
	view := postView{
		PostMeta:    meta,
		DisplayDate: s.cfg.FormatDate(meta.Published),
		MachineDate: meta.Published.Format(time.RFC3339),
	}

	entry, err := s.rendered(meta)
	if err != nil {
		return view, err
	}
	view.BodyHTML = entry.HTML

	return view, nil
}

// isFragmentRequest reports whether the caller wants bare markup rather than a
// full page.
//
// The distinction cannot be drawn from the query string, which is what an
// earlier version tried. Every paginated URL carries a cursor, including the
// one a reader lands on by clicking "Older posts" with JavaScript disabled, so
// keying off the cursor served that reader an unstyled fragment with no header
// and no navigation. The link worked and the page was broken, which is a worse
// failure than not having the enhancement at all.
//
// The script sets this header explicitly. A browser following an ordinary link
// never does, so a plain navigation always gets the full page and the
// progressive enhancement stays honest.
func isFragmentRequest(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "fetch"
}

// parseCursor reads the pagination cursor from the query string.
//
// A malformed cursor yields the zero value, which starts from the beginning.
// Returning an error instead would mean a stale bookmark shows an error page
// rather than the timeline, which is a worse outcome for what is only an
// optimization.
func parseCursor(r *http.Request) index.Cursor {
	raw := r.URL.Query().Get("before")
	if raw == "" {
		return index.Cursor{}
	}

	// The cursor is "timestamp,slug". A comma appears in neither part, since
	// timestamps are RFC3339 and slugs are letters, digits, and hyphens.
	timestamp, slug, found := strings.Cut(raw, ",")
	if !found {
		return index.Cursor{}
	}

	published, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return index.Cursor{}
	}

	return index.Cursor{Published: published, Slug: slug}
}

// encodeCursor renders a cursor for use in a URL.
func encodeCursor(c index.Cursor) string {
	if c.IsZero() {
		return ""
	}
	return c.Published.Format(time.RFC3339) + "," + c.Slug
}

// plural picks the right noun for a count.
func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}
