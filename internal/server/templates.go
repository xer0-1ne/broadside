package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Templates and static assets are compiled into the binary.
//
// This is what makes the distributable genuinely one file. There is no assets
// directory to deploy alongside it, no path to configure, and no way for the
// two to fall out of step during an upgrade. Replacing the binary replaces the
// templates and the stylesheet at the same instant.
//
//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

// sharedTemplates are parsed into every page's template set.
//
// layout.html is the page shell and partials.html holds the fragments that more
// than one page renders.
var sharedTemplates = []string{
	"templates/layout.html",
	"templates/partials.html",
	"templates/admin-shell.html",
}

// fragmentTemplates render without the surrounding page shell. These are the
// responses the infinite scroll script fetches and appends.
var fragmentTemplates = map[string]bool{
	"timeline-page.html": true,
}

// Templates holds the parsed template sets.
//
// Each page gets its own set rather than all pages sharing one. That is the
// important detail here: every page file defines a template called "content",
// and parsing them all into a single set would leave only the last one, so
// every route would render whichever page happened to be parsed last. Isolating
// each page means "content" refers to exactly one thing within its own set.
type Templates struct {
	pages map[string]*template.Template
}

// LoadTemplates parses every template at startup.
//
// Parsing once rather than per request is the obvious optimization, but the
// more valuable property is that a broken template fails at boot instead of on
// a request. A syntax error surfaces the moment the operator restarts, not the
// first time somebody visits a tag page.
func LoadTemplates() (*Templates, error) {
	entries, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("server: listing templates: %w", err)
	}

	shared := make(map[string]bool, len(sharedTemplates))
	for _, name := range sharedTemplates {
		shared[name] = true
	}

	pages := make(map[string]*template.Template)

	for _, entry := range entries {
		if shared[entry] {
			continue
		}

		name := strings.TrimPrefix(entry, "templates/")

		// Each page is parsed together with the shared files, producing a set
		// that contains the layout, the fragments, and exactly one "content".
		files := append(append([]string{}, sharedTemplates...), entry)

		set, err := template.New(name).Funcs(templateFuncs()).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("server: parsing %s: %w", name, err)
		}

		pages[name] = set
	}

	return &Templates{pages: pages}, nil
}

// templateFuncs are the helpers available inside templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// safeHTML marks already-sanitized markup as safe to emit unescaped.
		//
		// This is the one place the escaping guarantee is deliberately
		// bypassed, so it is worth being precise about why it is sound. The
		// only value ever passed here is the output of render.Render, which has
		// been through goldmark with raw HTML disabled and then through
		// bluemonday's allowlist. Passing anything else to this function would
		// be a serious bug, which is why it takes a string and is used in
		// exactly one template expression.
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},

		// year renders the current year, for the footer.
		"year": func() int {
			return time.Now().Year()
		},

		// archiveURL builds the link to a month's archive page. Doing this in Go
		// rather than by concatenating in the template keeps the zero padding
		// in one place, where the route pattern can be checked against it.
		"archiveURL": func(year int, month time.Month) string {
			return fmt.Sprintf("/archive/%04d/%02d", year, month)
		},

		// monthLabel renders a month and year for display.
		"monthLabel": func(year int, month time.Month) string {
			return fmt.Sprintf("%s %d", month.String(), year)
		},

		// hasPosts reports whether a slice has anything in it, which reads
		// better in a template than a length comparison.
		"hasPosts": func(posts []postView) bool {
			return len(posts) > 0
		},

		// dict builds a map so a template can be called with named arguments.
		//
		// Go templates pass a single value to a nested template, which makes a
		// reusable fragment taking several parameters awkward. This is the
		// conventional workaround, and it is used for the color field that the
		// settings form repeats six times.
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(pairs))
			}
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings, got %T", pairs[i])
				}
				out[key] = pairs[i+1]
			}
			return out, nil
		},
	}
}

// Render writes a named template to the response.
func (t *Templates) Render(w http.ResponseWriter, name string, data any) error {
	set, found := t.pages[name]
	if !found {
		return fmt.Errorf("server: no template named %s", name)
	}

	// Fragments render their own top-level definition. Full pages render the
	// layout, which pulls in the page's "content" block.
	entry := "layout"
	if fragmentTemplates[name] {
		entry = name
	}

	// The template is rendered into a buffer first so that a failure partway
	// through does not produce a half-written page with a 200 status already
	// committed. If it fails, nothing has been sent and a proper error response
	// is still possible.
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, entry, data); err != nil {
		return fmt.Errorf("server: rendering %s: %w", name, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// renderPage renders a template, handling any failure.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	if err := s.tmpl.Render(w, name, data); err != nil {
		s.logger.Error("rendering page", "template", name, "error", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
	}
}

// renderNotFound sends a 404 with the site's own styling.
func (s *Server) renderNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "That page does not exist.")
}

// renderError sends an error page.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	data := s.newPageData(r)
	data.Heading = http.StatusText(status)
	data.Subheading = message

	w.WriteHeader(status)
	if err := s.tmpl.Render(w, "error.html", data); err != nil {
		// The error template itself failed, so fall back to plain text rather
		// than recursing.
		s.logger.Error("rendering error page", "error", err)
		fmt.Fprintln(w, message)
	}
}

// writeJSON sends a JSON response.
func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		s.logger.Error("encoding JSON response", "error", err)
		http.Error(w, `{"error":"encoding failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(data)
}

// staticHandler serves the embedded assets.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// The path is a constant matching the embed directive, so this cannot
		// happen unless the build is broken.
		panic("server: static assets are missing from the binary: " + err.Error())
	}

	fileServer := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These are requested with a content hash in the query string, so a
		// changed file is a changed URL and the cached copy is never the wrong
		// one. That makes a long, immutable lifetime correct rather than
		// risky: a browser can keep it for a year and still never serve a
		// stylesheet that disagrees with the markup.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		// The embedded filesystem is read-only and contains only what was
		// committed, so there is nothing here a request could reach that was
		// not intended to be public.
		fileServer.ServeHTTP(w, r)
	})
}

// escapeXML escapes text for inclusion in a feed.
//
// The standard library's xml.EscapeText handles this, but it writes to an
// io.Writer and returns an error, which makes it awkward inside string
// building. This covers the five characters XML defines.
func escapeXML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
