package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/index"
)

// JSON Feed is the only feed format offered. RSS was removed deliberately.
//
// What remains is here because automation picking up new posts for syndication
// is an explicit goal, and parsing JSON in a script is considerably less
// painful than parsing XML.
//
// It is generated per request rather than cached. A feed is twenty posts of
// metadata already sitting in memory, so building it costs less than the
// bookkeeping caching it would require.

// feedItemLimit caps how many posts appear in a feed.
//
// Readers generally only care about recent items, and an unbounded feed on a
// site with years of history would be megabytes that get refetched on every
// poll.
const feedItemLimit = 20

// handleJSONFeed emits a JSON Feed version 1.1 document.
func (s *Server) handleJSONFeed(w http.ResponseWriter, r *http.Request) {
	page := s.index.Query(s.filter(r), index.Cursor{}, feedItemLimit)

	// The structures are declared inline because they exist only to be encoded
	// once. Naming them at package scope would suggest they are part of the
	// application's model, which they are not.
	type author struct {
		Name string `json:"name,omitempty"`
	}
	type item struct {
		ID            string   `json:"id"`
		URL           string   `json:"url"`
		Title         string   `json:"title"`
		Summary       string   `json:"summary,omitempty"`
		DatePublished string   `json:"date_published"`
		DateModified  string   `json:"date_modified,omitempty"`
		Tags          []string `json:"tags,omitempty"`
	}
	type feed struct {
		Version     string   `json:"version"`
		Title       string   `json:"title"`
		HomePageURL string   `json:"home_page_url,omitempty"`
		FeedURL     string   `json:"feed_url,omitempty"`
		Description string   `json:"description,omitempty"`
		Authors     []author `json:"authors,omitempty"`
		Language    string   `json:"language,omitempty"`
		Items       []item   `json:"items"`
	}

	document := feed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       s.cfg.Title,
		HomePageURL: s.cfg.AbsoluteURL("/"),
		FeedURL:     s.cfg.AbsoluteURL("/feed.json"),
		Description: s.cfg.Slogan,
		Language:    s.cfg.Language,
		Items:       make([]item, 0, len(page.Posts)),
	}

	if s.cfg.DisplayName != "" {
		document.Authors = []author{{Name: s.cfg.DisplayName}}
	}

	for _, post := range page.Posts {
		entry := item{
			ID:            s.cfg.AbsoluteURL(post.URL),
			URL:           s.cfg.AbsoluteURL(post.URL),
			Title:         post.Title,
			Summary:       s.feedSummary(post),
			DatePublished: post.Published.Format(time.RFC3339),
			Tags:          post.Tags,
		}
		if !post.Updated.IsZero() {
			entry.DateModified = post.Updated.Format(time.RFC3339)
		}
		document.Items = append(document.Items, entry)
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	s.writeJSON(w, http.StatusOK, document)
}

// feedSummary returns the text describing a post in a feed.
//
// The frontmatter summary is used when present. Otherwise one is generated from
// the body, which means reading the file, so this is the reason a site with
// many posts and no summaries makes the feed the most expensive route. Writing
// summaries is the fix, and it produces better feeds anyway.
func (s *Server) feedSummary(post index.PostMeta) string {
	if post.Summary != "" {
		return post.Summary
	}

	full, err := s.store.Read(post.Path)
	if err != nil {
		return ""
	}
	return s.renderer.Excerpt(full.Body, 300)
}

// handleSitemap emits a sitemap for crawlers.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	// A sitemap of relative URLs is meaningless to a crawler, so without a
	// configured base URL there is nothing useful to produce.
	if s.cfg.BaseURL == "" {
		s.renderNotFound(w, r)
		return
	}

	// Every post is listed rather than one page of them, which is the point of
	// a sitemap. The limit is high rather than absent so that a runaway content
	// folder cannot produce an unbounded response.
	page := s.index.Query(s.filter(r), index.Cursor{}, 50000)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	fmt.Fprintf(&b, "<url><loc>%s</loc></url>\n", escapeXML(s.cfg.AbsoluteURL("/")))

	for _, post := range page.Posts {
		modified := post.Published
		if !post.Updated.IsZero() {
			modified = post.Updated
		}
		fmt.Fprintf(&b, "<url><loc>%s</loc><lastmod>%s</lastmod></url>\n",
			escapeXML(s.cfg.AbsoluteURL(post.URL)),
			modified.Format("2006-01-02"))
	}

	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write([]byte(b.String()))
}
