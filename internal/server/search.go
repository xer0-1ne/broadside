package server

import (
	"net/http"
	"strings"

	"git.thebytes.net/roberts/broadside/internal/content"
	"git.thebytes.net/roberts/broadside/internal/index"
)

// Search runs in one of two modes, which the reader chooses with the buttons
// that appear under the search field.
//
// Splitting the two is worth the small extra surface because they answer
// genuinely different questions. Searching tags is browsing: the reader knows
// the topic exists and wants everything filed under it, so matching against
// post bodies would bury the result they were after under every post that
// happened to mention the word. Searching content is looking for a half
// remembered sentence, where tags are useless.
//
// Combining both into one ranked query is the usual approach, and it is worse
// here: ranking needs tuning nobody will do, and a wrong guess about intent is
// invisible to the reader, who just sees the wrong results.

// SearchMode selects what a query is matched against.
type SearchMode string

const (
	// SearchTags matches the query against tag names only.
	SearchTags SearchMode = "tags"

	// SearchContent matches against titles and full post bodies.
	SearchContent SearchMode = "content"
)

// parseSearchMode reads the mode from a request, defaulting to content.
//
// Content is the default because it is the mode that always returns something
// useful: a reader who types a topic name gets the posts about it either way,
// whereas tag mode returns nothing at all if the author never created that tag.
func parseSearchMode(r *http.Request) SearchMode {
	if strings.EqualFold(r.URL.Query().Get("mode"), string(SearchTags)) {
		return SearchTags
	}
	return SearchContent
}

// search returns the posts matching a query under the given mode.
//
// Results keep the timeline's ordering rather than being ranked by relevance.
// For a personal blog, "the most recent post that mentions this" is almost
// always what the reader wants, and a relevance score computed over a few
// hundred documents mostly produces surprising orderings that are hard to
// explain.
func (s *Server) search(query string, mode SearchMode, filter index.Filter) []index.PostMeta {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil
	}

	// The whole index is scanned. That is acceptable at the scale this product
	// targets, and it avoids maintaining an inverted index that would have to
	// stay correct across writes, external edits, and reindexing.
	all := s.index.Query(filter, index.Cursor{}, maxSearchScan)

	if mode == SearchTags {
		return s.searchTags(all.Posts, query)
	}
	return s.searchContent(all.Posts, query)
}

// maxSearchScan bounds how many posts a single query examines.
//
// Content search reads and renders anything not already cached, so an
// unbounded scan on a cold cache would let one request walk the entire archive.
// Five thousand posts is far past what this product is aimed at and still
// finishes quickly.
const maxSearchScan = 5000

// searchTags matches against tag names.
//
// The query is slugified first so that "Astro Photography" finds the tag
// "astro-photography". Without that, searching for a tag exactly as it appears
// on the page would fail, which is the most obvious thing a reader will try.
func (s *Server) searchTags(posts []index.PostMeta, query string) []index.PostMeta {
	slug := content.Slugify(query)

	results := make([]index.PostMeta, 0, 16)
	for _, post := range posts {
		for _, tag := range post.Tags {
			// Substring rather than exact, so "astro" finds "astrophotography"
			// and a reader does not have to know the full tag to reach it.
			if slug != "" && strings.Contains(tag, slug) {
				results = append(results, post)
				break
			}
		}
	}
	return results
}

// searchContent matches against titles, summaries, and full post bodies.
func (s *Server) searchContent(posts []index.PostMeta, query string) []index.PostMeta {
	results := make([]index.PostMeta, 0, 16)

	for _, post := range posts {
		// Title and summary are checked first because they are already in
		// memory, and a title match avoids touching the body at all.
		if strings.Contains(strings.ToLower(post.Title), query) ||
			strings.Contains(strings.ToLower(post.Summary), query) {
			results = append(results, post)
			continue
		}

		// The body comes from the render cache, which holds a lowercased
		// plain-text copy for exactly this. On a warm cache no disk access
		// happens at all.
		entry, err := s.rendered(post)
		if err != nil {
			// A post that cannot be read is skipped rather than failing the
			// search. It is already reported when the timeline tries to show
			// it.
			continue
		}

		if strings.Contains(entry.Text, query) {
			results = append(results, post)
		}
	}

	return results
}
