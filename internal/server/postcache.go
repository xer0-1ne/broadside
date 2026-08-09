package server

import (
	"strings"
	"sync"
	"time"

	"git.thebytes.net/roberts/broadside/internal/index"
	"github.com/microcosm-cc/bluemonday"
)

// postCache holds rendered post bodies in memory.
//
// This exists because the timeline shows entire posts rather than summaries.
// Without a cache, one request for a page of twenty posts means twenty file
// reads and twenty markdown parses, repeated for every visitor and every
// crawler. Markdown rendering is not slow in isolation, but doing it forty
// thousand times an hour on a small VPS is a self-inflicted wound.
//
// The cache holds what the index deliberately does not. The index stays small
// so that twenty thousand posts cost a few megabytes; this holds rendered
// bodies, which are much larger, and is therefore bounded by count rather than
// growing with the archive.
//
// Invalidation is by modification time. Every lookup stats the file, which is a
// cheap syscall, and a changed timestamp discards the entry. That means a post
// edited by hand, dropped in over Syncthing, or pulled from git is picked up on
// the next request with no watcher involved. Once fsnotify lands it can evict
// directly and the stat becomes belt and braces.
type postCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry

	// order records insertion sequence for eviction. A proper LRU would track
	// access instead, but that means writing on every read, which turns a
	// read-mostly structure into a write-mostly one. Evicting the oldest entry
	// is good enough when the working set is "the current page of the
	// timeline".
	order []string

	limit int
}

// cacheEntry is one rendered post.
type cacheEntry struct {
	// HTML is the sanitized rendered body.
	HTML string

	// Text is the body stripped to plain text, lowercased, used for content
	// search. Keeping it here means a full-text query scans memory instead of
	// re-reading and re-rendering every file on the disk.
	Text string

	// Summary is the generated excerpt, used where a post has none of its own.
	Summary string

	// modTime is what the entry was built from. A file whose timestamp no
	// longer matches this has been edited.
	modTime time.Time
}

// newPostCache creates a cache holding at most limit entries.
func newPostCache(limit int) *postCache {
	if limit <= 0 {
		limit = 500
	}
	return &postCache{
		entries: make(map[string]*cacheEntry, limit),
		limit:   limit,
	}
}

// get returns a cached entry when it is still valid for the given
// modification time.
func (c *postCache) get(path string, modTime time.Time) (*cacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.entries[path]
	if !found {
		return nil, false
	}

	// A file rewritten in the same second as its last render would keep a stale
	// entry if this compared only whole seconds, so the comparison uses the
	// full timestamp. Equal is used rather than == because time.Time carries a
	// monotonic clock reading that must not participate.
	if !entry.modTime.Equal(modTime) {
		return nil, false
	}

	return entry, true
}

// put stores an entry, evicting the oldest if the cache is full.
func (c *postCache) put(path string, entry *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[path]; !exists {
		c.order = append(c.order, path)
	}
	c.entries[path] = entry

	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// invalidate drops one entry, for use when a write is known to have happened.
func (c *postCache) invalidate(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, path)
	for i, existing := range c.order {
		if existing == path {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// clear empties the cache, which a full reindex uses.
func (c *postCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	clear(c.entries)
	c.order = c.order[:0]
}

// rendered returns the cached render for a post, producing it on a miss.
//
// This is the single path through which post bodies reach a template, so every
// consumer benefits from the cache without having to know it exists.
func (s *Server) rendered(meta index.PostMeta) (*cacheEntry, error) {
	// Stat first so the cached entry can be checked against the file's current
	// state. A missing file is reported to the caller, which skips the post
	// rather than failing the whole page.
	info, err := s.store.Root().Stat("posts/" + meta.Path)
	if err != nil {
		return nil, err
	}
	modTime := info.ModTime()

	if entry, found := s.cache.get(meta.Path, modTime); found {
		return entry, nil
	}

	post, err := s.store.Read(meta.Path)
	if err != nil {
		return nil, err
	}

	html, err := s.renderer.Render(post.Body)
	if err != nil {
		return nil, err
	}

	// The plain-text form is derived from the rendered HTML rather than from
	// the markdown source, so a search for "hello" is not defeated by the
	// author having written "hel*lo*". It is lowercased once here instead of on
	// every query.
	text := bluemonday.StripTagsPolicy().Sanitize(html)
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))

	entry := &cacheEntry{
		HTML:    html,
		Text:    text,
		Summary: s.renderer.Excerpt(post.Body, 240),
		modTime: modTime,
	}

	s.cache.put(meta.Path, entry)
	return entry, nil
}

// postCacheSize is how many rendered posts are held at once.
//
// The working set is whatever is on screen, so this only has to comfortably
// exceed a page of the timeline. Five hundred entries covers twenty-five pages
// at the default page size, which means ordinary browsing never evicts anything
// it is about to need. At a few kilobytes of HTML per post that is single-digit
// megabytes in the worst case.
const postCacheSize = 500
