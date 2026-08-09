package server

import (
	"net/http"
	"sort"
	"time"

	"git.thebytes.net/roberts/broadside/internal/auth"
	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/content"
	"git.thebytes.net/roberts/broadside/internal/index"
)

// The admin is four tabs, each a real route rather than a panel swapped in by
// script.
//
// That means every tab has its own URL, can be bookmarked and linked, and works
// with the back button and with JavaScript disabled. A tabbed interface built
// from divs and a click handler gets none of those for free and has to
// reimplement each one badly.

// adminTab identifies which tab is active, for highlighting in the shell.
type adminTab string

const (
	tabContent  adminTab = "content"
	tabMedia    adminTab = "media"
	tabSettings adminTab = "settings"
	tabAPI      adminTab = "api"
)

// adminTabLink is one entry in the tab strip.
type adminTabLink struct {
	Tab    adminTab
	Label  string
	URL    string
	Active bool
}

// adminTabs builds the tab strip with the current one marked.
func adminTabs(active adminTab) []adminTabLink {
	all := []adminTabLink{
		{Tab: tabContent, Label: "Content", URL: "/admin"},
		{Tab: tabMedia, Label: "Media", URL: "/admin/media"},
		{Tab: tabSettings, Label: "Site Settings", URL: "/admin/settings"},
		{Tab: tabAPI, Label: "API", URL: "/admin/api"},
	}
	for i := range all {
		all[i].Active = all[i].Tab == active
	}
	return all
}

// adminData carries the admin-only fields.
//
// It hangs off pageData rather than embedding it. Embedding, and then pointing
// the embedded copy back at the whole struct, produced a value that contained
// itself and was confusing to read for no benefit. The templates reach these
// through .Admin.
type adminData struct {
	Tabs   []adminTabLink
	CSRF   string
	Notice string

	// Content tab.
	Drafts    []adminPost
	Published []adminPost
	Scheduled []adminPost
	Editing   *content.Post
	EditPath  string

	// Media tab.
	Uploads []mediaFile

	// Settings tab.
	Settings         config.Config
	Username         string
	Email            string
	SiteTitleFonts   []FontOption
	PostTitleFonts   []FontOption
	ContentFonts     []FontOption
	UploadedFonts    []UploadedFont
	AllPlatforms     []Platform
	CustomIcons      []string
	SocialEntries    []config.SocialLink
	DateExamples     []config.DateExample
	DefaultFooter    string
	MaxUploadCeiling int
	MinPasswordFloor int

	// API tab.
	Tokens   []auth.Token
	NewToken string
	BaseURL  string
	Stats    index.Stats
}

// adminPost is a post as the content list shows it.
type adminPost struct {
	index.PostMeta
	DisplayDate string
	Status      string
}

// newAdminPage assembles the shared parts of every admin page.
func (s *Server) newAdminPage(r *http.Request, active adminTab) (pageData, *adminData) {
	page := s.newPageData(r)
	page.LoggedIn = true

	admin := &adminData{Tabs: adminTabs(active)}

	if session, ok := s.currentSession(r); ok {
		admin.CSRF = session.CSRF
	}

	// A notice survives a redirect through the query string rather than a flash
	// cookie. It is a short confirmation, not a secret, and this avoids keeping
	// server-side state for a message that is read once.
	admin.Notice = r.URL.Query().Get("notice")

	page.Admin = admin
	return page, admin
}

// handleAdminContent lists posts and is the admin landing page.
func (s *Server) handleAdminContent(w http.ResponseWriter, r *http.Request) {
	page, admin := s.newAdminPage(r, tabContent)
	page.Heading = "Content"

	// Everything, including drafts and scheduled posts, since this is the
	// author's own view.
	results := s.index.Query(index.AdminFilter, index.Cursor{}, 5000)
	now := time.Now().In(s.cfg.Location())

	for _, post := range results.Posts {
		entry := adminPost{
			PostMeta:    post,
			DisplayDate: post.Published.Format("2 Jan 2006, 15:04"),
		}

		switch {
		case post.Draft:
			entry.Status = "Draft"
			admin.Drafts = append(admin.Drafts, entry)
		case post.Published.After(now):
			entry.Status = "Scheduled"
			admin.Scheduled = append(admin.Scheduled, entry)
		default:
			entry.Status = "Published"
			admin.Published = append(admin.Published, entry)
		}
	}

	s.renderPage(w, r, "admin-content.html", page)
}

// handleAdminMedia lists uploaded files.
func (s *Server) handleAdminMedia(w http.ResponseWriter, r *http.Request) {
	page, admin := s.newAdminPage(r, tabMedia)
	page.Heading = "Media"

	files, err := s.listUploads()
	if err != nil {
		s.logger.Error("listing uploads", "error", err)
	}
	admin.Uploads = files

	s.renderPage(w, r, "admin-media.html", page)
}

// handleAdminAPI documents the integration surface and manages tokens.
func (s *Server) handleAdminAPI(w http.ResponseWriter, r *http.Request) {
	page, admin := s.newAdminPage(r, tabAPI)
	page.Heading = "API"
	admin.Tokens = s.authStore.Tokens()
	admin.Stats = s.index.Stats()

	// The examples need an absolute address to be copy-pasteable. Falling back
	// to the request's own host means they work during local development,
	// before base_url has been filled in.
	admin.BaseURL = s.cfg.BaseURL
	if admin.BaseURL == "" {
		scheme := "http"
		if s.isSecure(r) {
			scheme = "https"
		}
		admin.BaseURL = scheme + "://" + r.Host
	}

	// A newly minted token is passed through the query string exactly once so
	// it can be shown after the redirect. It is never stored and never appears
	// again, which is the whole point of the create-once model.
	admin.NewToken = r.URL.Query().Get("token")

	sort.Slice(admin.Tokens, func(i, j int) bool {
		return admin.Tokens[i].Created.After(admin.Tokens[j].Created)
	})

	s.renderPage(w, r, "admin-api.html", page)
}

// handleAdminTokenCreate issues a new API token.
func (s *Server) handleAdminTokenCreate(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	secret, token, err := s.authStore.CreateToken(r.FormValue("name"))
	if err != nil {
		s.logger.Error("creating API token", "error", err)
		http.Error(w, "The token could not be created.", http.StatusInternalServerError)
		return
	}

	s.logger.Info("API token created", "name", token.Name, "id", token.ID)

	// The secret rides one redirect so the page can display it once. That does
	// put it in a URL, which is normally the wrong place for a credential, so
	// it is worth being explicit about why it is acceptable here: the request
	// is same-origin over the operator's own session, the response is not
	// cacheable, and nothing links onward from this page so no referrer carries
	// it. The alternative, server-side flash state, is more machinery for a
	// value that exists for one render.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/admin/api?token="+secret, http.StatusSeeOther)
}

// handleAdminTokenRevoke deletes an API token.
func (s *Server) handleAdminTokenRevoke(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	id := r.FormValue("id")
	if err := s.authStore.RevokeToken(id); err != nil {
		s.logger.Error("revoking API token", "id", id, "error", err)
		http.Error(w, "The token could not be revoked.", http.StatusInternalServerError)
		return
	}

	s.logger.Info("API token revoked", "id", id)
	http.Redirect(w, r, "/admin/api?notice=Token+revoked", http.StatusSeeOther)
}
