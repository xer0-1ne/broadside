package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/content"
	"git.thebytes.net/roberts/broadside/internal/index"
)

// Writing a post goes through one path whether it came from the admin form or
// from an API client, because two paths would mean two sets of validation and
// the weaker one would become the way in.
//
// The admin editor edits markdown source directly rather than through a block
// editor. That is what makes the round trip exact by construction: the file is
// the document, so there is no intermediate representation to lose information
// on the way in or out. A block editor would need every block type to map
// losslessly to markdown, and each one that did not would be a data loss bug
// waiting for the right post to trigger it.

// handleAdminNewPost renders an empty editor.
func (s *Server) handleAdminNewPost(w http.ResponseWriter, r *http.Request) {
	page, admin := s.newAdminPage(r, tabContent)
	page.Heading = "New post"

	admin.Editing = &content.Post{
		Frontmatter: content.Frontmatter{
			Published: time.Now().In(s.store.Location()),
		},
	}

	s.renderPage(w, r, "admin-edit.html", page)
}

// handleAdminEditPost loads an existing post into the editor.
func (s *Server) handleAdminEditPost(w http.ResponseWriter, r *http.Request) {
	storagePath := r.PathValue("path")

	post, err := s.store.Read(storagePath)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}

	page, admin := s.newAdminPage(r, tabContent)
	page.Heading = "Editing"
	admin.Editing = &post
	admin.EditPath = storagePath

	s.renderPage(w, r, "admin-edit.html", page)
}

// handleAdminSavePost writes the editor form.
func (s *Server) handleAdminSavePost(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	storagePath := r.FormValue("path")

	post := content.Post{
		Frontmatter: content.Frontmatter{
			Title:   strings.TrimSpace(r.FormValue("title")),
			Slug:    content.Slugify(r.FormValue("slug")),
			Summary: strings.TrimSpace(r.FormValue("summary")),
			Cover:   strings.TrimSpace(r.FormValue("cover")),
			Draft:   r.FormValue("draft") == "on",
			Tags:    parseTagField(r.FormValue("tags")),
		},
		// The body is taken exactly as typed. Normalizing it here would edit
		// somebody's writing behind their back, and markdown already treats
		// trailing whitespace as meaningful in one case.
		Body: normalizeNewlines(r.FormValue("body")),
	}

	if published := strings.TrimSpace(r.FormValue("published")); published != "" {
		if when, err := content.ParseTime(published, s.store.Location()); err == nil {
			post.Published = when
		}
	}
	if post.Published.IsZero() {
		post.Published = time.Now().In(s.store.Location())
	}

	if post.Title == "" {
		post.Title = "Untitled"
	}

	var err error
	if storagePath == "" {
		storagePath, err = s.store.Create(post)
	} else {
		// Unknown frontmatter keys are not in the form, so they would be lost
		// on save. Reading the existing post first and carrying its extras
		// across is what keeps the round-trip promise for anything a
		// third-party client wrote into the header.
		if existing, readErr := s.store.Read(storagePath); readErr == nil {
			post.Extra = existing.Extra
		}
		err = s.store.Update(storagePath, post)
	}

	if err != nil {
		s.logger.Error("saving post", "path", storagePath, "error", err)
		http.Error(w, "That post could not be saved.", http.StatusInternalServerError)
		return
	}

	s.afterWrite(storagePath)
	s.logger.Info("post saved", "path", storagePath, "username", session.Username)

	http.Redirect(w, r, "/admin?notice=Post+saved", http.StatusSeeOther)
}

// handleAdminDeletePost removes a post.
func (s *Server) handleAdminDeletePost(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	storagePath := r.FormValue("path")
	if err := s.store.Delete(storagePath); err != nil {
		s.logger.Error("deleting post", "path", storagePath, "error", err)
		http.Redirect(w, r, "/admin?notice=That+post+could+not+be+deleted", http.StatusSeeOther)
		return
	}

	s.afterWrite(storagePath)
	s.logger.Info("post deleted", "path", storagePath, "username", session.Username)

	http.Redirect(w, r, "/admin?notice=Post+deleted", http.StatusSeeOther)
}

// afterWrite brings the index and the render cache back in step with the disk.
//
// This is the same path the folder watcher takes, which is the point: a post
// written here and a file dropped in from outside end up in identical states,
// so there is no route through which one can be visible and the other not.
func (s *Server) afterWrite(storagePath string) {
	if storagePath != "" {
		s.cache.invalidate(storagePath)
	}
	s.Reload()
}

// parseTagField reads the comma-separated tag input from the editor.
func parseTagField(value string) []string {
	var tags []string
	seen := make(map[string]struct{})

	for _, raw := range strings.Split(value, ",") {
		tag := content.Slugify(raw)
		if tag == "" {
			continue
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

// normalizeNewlines converts the carriage returns a browser form submits.
//
// Every browser posts textarea content with CRLF line endings regardless of the
// platform, so without this every post saved through the admin would gain a
// carriage return on every line and show up as a whole-file change in git.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// ---- The JSON API ------------------------------------------------------

// apiPost is the wire form of a post.
type apiPost struct {
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Published time.Time `json:"published"`

	// A pointer, because omitempty does not skip a zero time.Time. A struct is
	// never "empty" as far as encoding/json is concerned, so the plain form
	// emitted "0001-01-01T00:00:00Z" for every post that had never been
	// edited, which is a date a client might reasonably try to use.
	Updated *time.Time `json:"updated,omitempty"`

	Draft   bool     `json:"draft"`
	Tags    []string `json:"tags,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Cover   string   `json:"cover,omitempty"`
	Body    string   `json:"body,omitempty"`
	URL     string   `json:"url"`
	Path    string   `json:"path"`
}

// handleAPIListPosts returns posts, cursor paginated.
func (s *Server) handleAPIListPosts(w http.ResponseWriter, r *http.Request) {
	filter := index.AdminFilter
	if r.URL.Query().Get("drafts") == "false" {
		filter = index.PublicFilter
	}

	limit := 20
	if n, err := parseIntParam(r, "limit"); err == nil && n > 0 && n <= 100 {
		limit = n
	}

	page := s.index.Query(filter, parseCursor(r), limit)

	posts := make([]apiPost, 0, len(page.Posts))
	for _, meta := range page.Posts {
		posts = append(posts, apiPostFromMeta(meta))
	}

	response := map[string]any{"posts": posts}
	if page.HasMore {
		// The cursor is handed back rather than a page number, so a client
		// polling for new posts cannot skip or repeat one when something is
		// published mid-scroll.
		response["next"] = encodeCursor(page.Next)
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleAPIGetPost returns a single post including its body.
func (s *Server) handleAPIGetPost(w http.ResponseWriter, r *http.Request) {
	meta, found := s.findBySlug(r.PathValue("slug"))
	if !found {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such post"})
		return
	}

	post, err := s.store.Read(meta.Path)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "that post could not be read"})
		return
	}

	out := apiPostFromMeta(meta)
	out.Body = post.Body
	s.writeJSON(w, http.StatusOK, out)
}

// handleAPICreatePost publishes a post from JSON.
func (s *Server) handleAPICreatePost(w http.ResponseWriter, r *http.Request) {
	var in apiPost
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that request body could not be read"})
		return
	}

	if strings.TrimSpace(in.Title) == "" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	post := content.Post{
		Frontmatter: content.Frontmatter{
			Title:     strings.TrimSpace(in.Title),
			Slug:      content.Slugify(in.Slug),
			Published: in.Published,
			Draft:     in.Draft,
			Tags:      normalizeTags(in.Tags),
			Summary:   in.Summary,
			Cover:     in.Cover,
		},
		Body: in.Body,
	}

	storagePath, err := s.store.Create(post)
	if err != nil {
		s.logger.Error("creating post via API", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "that post could not be created"})
		return
	}

	s.afterWrite(storagePath)
	s.logger.Info("post created via API", "path", storagePath)

	if meta, found := s.findByPath(storagePath); found {
		s.writeJSON(w, http.StatusCreated, apiPostFromMeta(meta))
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]string{"path": storagePath})
}

// handleAPIUpdatePost edits an existing post.
func (s *Server) handleAPIUpdatePost(w http.ResponseWriter, r *http.Request) {
	meta, found := s.findBySlug(r.PathValue("slug"))
	if !found {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such post"})
		return
	}

	existing, err := s.store.Read(meta.Path)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "that post could not be read"})
		return
	}

	// Decoding onto the existing values makes this a genuine partial update:
	// a field the client omits keeps whatever it had, rather than being reset
	// to a zero value. That matters for a scripted client that only wants to
	// flip the draft flag.
	patch := apiPostFromPost(existing, meta)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that request body could not be read"})
		return
	}

	existing.Title = patch.Title
	existing.Slug = content.Slugify(patch.Slug)
	existing.Draft = patch.Draft
	existing.Tags = normalizeTags(patch.Tags)
	existing.Summary = patch.Summary
	existing.Cover = patch.Cover
	existing.Body = patch.Body
	if !patch.Published.IsZero() {
		existing.Published = patch.Published
	}

	if err := s.store.Update(meta.Path, existing); err != nil {
		s.logger.Error("updating post via API", "path", meta.Path, "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "that post could not be saved"})
		return
	}

	s.afterWrite(meta.Path)
	s.logger.Info("post updated via API", "path", meta.Path)

	if updated, ok := s.findByPath(meta.Path); ok {
		s.writeJSON(w, http.StatusOK, apiPostFromMeta(updated))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"path": meta.Path})
}

// handleAPIDeletePost removes a post.
func (s *Server) handleAPIDeletePost(w http.ResponseWriter, r *http.Request) {
	meta, found := s.findBySlug(r.PathValue("slug"))
	if !found {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such post"})
		return
	}

	if err := s.store.Delete(meta.Path); err != nil {
		s.logger.Error("deleting post via API", "path", meta.Path, "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "that post could not be deleted"})
		return
	}

	s.afterWrite(meta.Path)
	s.logger.Info("post deleted via API", "path", meta.Path)

	s.writeJSON(w, http.StatusOK, map[string]string{"deleted": meta.Path})
}

// handleAPIListMedia lists uploaded files.
func (s *Server) handleAPIListMedia(w http.ResponseWriter, r *http.Request) {
	files, err := s.listUploads()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the media library could not be read"})
		return
	}

	type item struct {
		URL      string    `json:"url"`
		Name     string    `json:"name"`
		Size     int64     `json:"size"`
		Modified time.Time `json:"modified"`
	}

	items := make([]item, 0, len(files))
	for _, file := range files {
		items = append(items, item{URL: file.URL, Name: file.Name, Size: file.Size, Modified: file.Modified})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"media": items})
}

// findBySlug locates a post by slug alone.
//
// The index is keyed by date and slug, because slugs are only unique within a
// day. The API takes a bare slug for convenience, so this scans for it and
// takes the newest match, which is what somebody typing a slug into a script
// means.
func (s *Server) findBySlug(slug string) (index.PostMeta, bool) {
	slug = content.Slugify(slug)
	if slug == "" {
		return index.PostMeta{}, false
	}

	for _, post := range s.index.All() {
		if post.Slug == slug {
			return post, true
		}
	}
	return index.PostMeta{}, false
}

// findByPath locates a post by its storage path.
func (s *Server) findByPath(storagePath string) (index.PostMeta, bool) {
	for _, post := range s.index.All() {
		if post.Path == storagePath {
			return post, true
		}
	}
	return index.PostMeta{}, false
}

func apiPostFromMeta(meta index.PostMeta) apiPost {
	var updated *time.Time
	if !meta.Updated.IsZero() {
		updated = &meta.Updated
	}

	return apiPost{
		Slug:      meta.Slug,
		Title:     meta.Title,
		Published: meta.Published,
		Updated:   updated,
		Draft:     meta.Draft,
		Tags:      meta.Tags,
		Summary:   meta.Summary,
		Cover:     meta.Cover,
		URL:       meta.URL,
		Path:      meta.Path,
	}
}

func apiPostFromPost(post content.Post, meta index.PostMeta) apiPost {
	out := apiPostFromMeta(meta)
	out.Body = post.Body
	return out
}

// normalizeTags puts API-supplied tags through the same slugifier the parser
// uses, so a tag created through the API and one written by hand match.
func normalizeTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		if slug := content.Slugify(tag); slug != "" {
			out = append(out, slug)
		}
	}
	return out
}

// parseIntParam reads an integer query parameter.
func parseIntParam(r *http.Request, name string) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return 0, errors.New("absent")
	}

	n := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
