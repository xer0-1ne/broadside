// Package index holds every post's metadata in memory and answers the queries
// the site makes of it.
//
// This is the piece that decides whether the platform is fast or unusable.
// Scanning the posts directory on every request works fine for a demo and falls
// apart somewhere around a few hundred posts, and infinite scroll hammers that
// path harder than anything else on the site. So the metadata is read once,
// kept in memory, and updated in place from then on.
//
// The cost is small enough not to think about. A PostMeta is roughly two
// hundred bytes, so twenty thousand posts is a few megabytes, which is less
// than the Go runtime uses to start up. Bodies are never held: they are read
// from disk when a single post is rendered, which is one file read for one
// request rather than thousands of reads for every request.
package index

import (
	"sort"
	"strings"
	"sync"
	"time"

	"git.thebytes.net/roberts/broadside/internal/content"
)

// PostMeta is everything the index knows about a post.
//
// It deliberately excludes the body. Holding bodies would turn a few megabytes
// into a few hundred, and the timeline never needs them: it shows a title, a
// date, and a summary.
type PostMeta struct {
	Slug      string
	Title     string
	Published time.Time
	Updated   time.Time
	Draft     bool
	Tags      []string
	Summary   string
	Cover     string

	// Path is the storage path relative to the posts directory, which is how
	// the store is asked for the body when a single post is rendered.
	Path string

	// URL is the canonical public address, precomputed because it is needed on
	// every timeline render and deriving it means parsing the path again.
	URL string
}

// Index is the in-memory catalogue of posts.
//
// Reads vastly outnumber writes, which is why this uses an RWMutex: any number
// of requests can page through the timeline at once, and a save briefly blocks
// them rather than each request queueing behind the others.
type Index struct {
	mu sync.RWMutex

	// posts is sorted descending by published time, with slug as a tiebreaker.
	// Keeping it sorted at all times means pagination is a slice operation
	// rather than a sort per request.
	posts []PostMeta

	// bySlug locates a post without scanning. Slugs are unique per day rather
	// than globally, so the key includes the date; see slugKey.
	bySlug map[string]int

	// loc is the site timezone, used to decide whether a scheduled post has
	// arrived yet.
	loc *time.Location
}

// New creates an empty index.
func New(loc *time.Location) *Index {
	if loc == nil {
		loc = time.UTC
	}
	return &Index{
		bySlug: make(map[string]int),
		loc:    loc,
	}
}

// slugKey builds the lookup key for a post.
//
// A slug is only unique within its day, since the collision rules in the naming
// layer are scoped to a day directory, so the date has to be part of the key.
// Two posts called "weekly-notes" on different days are different posts and
// both keep the clean slug.
func slugKey(year int, month time.Month, day int, slug string) string {
	var b strings.Builder
	b.Grow(len(slug) + 11)
	b.WriteString(formatDate(year, month, day))
	b.WriteByte('/')
	b.WriteString(slug)
	return b.String()
}

// formatDate renders a date as YYYY/MM/DD without allocating through fmt.
func formatDate(year int, month time.Month, day int) string {
	buf := []byte("0000/00/00")
	buf[0] = byte('0' + year/1000%10)
	buf[1] = byte('0' + year/100%10)
	buf[2] = byte('0' + year/10%10)
	buf[3] = byte('0' + year%10)
	buf[5] = byte('0' + byte(month)/10)
	buf[6] = byte('0' + byte(month)%10)
	buf[8] = byte('0' + byte(day)/10)
	buf[9] = byte('0' + byte(day)%10)
	return string(buf)
}

// Key returns the index key for a post.
func (m PostMeta) Key() string {
	year, month, day := m.Published.Date()
	return slugKey(year, month, day, m.Slug)
}

// Replace swaps in a completely new set of posts.
//
// This is what a full rebuild calls. Building the replacement slice before
// taking the lock keeps the write window to the length of a sort rather than
// the length of a directory walk, so requests are not blocked while the disk
// is being read.
func (idx *Index) Replace(posts []PostMeta) {
	sortPosts(posts)

	bySlug := make(map[string]int, len(posts))
	for i, post := range posts {
		bySlug[post.Key()] = i
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.posts = posts
	idx.bySlug = bySlug
}

// Upsert adds or replaces a single post.
//
// This is what a save calls, so that publishing does not trigger a rescan of
// the whole content folder. The cost is a sort of an already-sorted slice,
// which is close to free, plus rebuilding the lookup map.
func (idx *Index) Upsert(post PostMeta) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if i, exists := idx.bySlug[post.Key()]; exists {
		idx.posts[i] = post
	} else {
		idx.posts = append(idx.posts, post)
	}

	// A new post is almost always the newest, so the slice is nearly sorted and
	// this is much cheaper than the general case suggests.
	sortPosts(idx.posts)
	idx.reindexLocked()
}

// Remove deletes a post from the index.
func (idx *Index) Remove(key string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	i, exists := idx.bySlug[key]
	if !exists {
		return
	}

	idx.posts = append(idx.posts[:i], idx.posts[i+1:]...)
	idx.reindexLocked()
}

// reindexLocked rebuilds the slug lookup after the slice has shifted. The
// caller must already hold the write lock.
func (idx *Index) reindexLocked() {
	clear(idx.bySlug)
	for i, post := range idx.posts {
		idx.bySlug[post.Key()] = i
	}
}

// sortPosts orders posts newest first.
//
// The slug tiebreaker is what makes the order total rather than merely
// approximate. Two posts can share a published timestamp, either because they
// were imported with date-only frontmatter or because they were created in the
// same second, and without a deterministic tiebreaker their relative order
// could change between sorts. That would make cursor pagination skip or repeat
// them, since the cursor relies on the order being stable.
func sortPosts(posts []PostMeta) {
	sort.SliceStable(posts, func(i, j int) bool {
		if !posts[i].Published.Equal(posts[j].Published) {
			return posts[i].Published.After(posts[j].Published)
		}
		return posts[i].Slug < posts[j].Slug
	})
}

// Filter describes which posts a query should consider.
type Filter struct {
	// IncludeDrafts admits posts marked draft. The public site never sets this;
	// the admin views do.
	IncludeDrafts bool

	// IncludeScheduled admits posts whose published time is still in the
	// future. Same idea: hidden publicly, visible to the author.
	IncludeScheduled bool

	// Tag restricts results to posts carrying it. Empty means no restriction.
	Tag string
}

// PublicFilter is what an unauthenticated request uses.
var PublicFilter = Filter{}

// AdminFilter shows everything, including drafts and scheduled posts.
var AdminFilter = Filter{IncludeDrafts: true, IncludeScheduled: true}

// Matches reports whether a post satisfies the filter as of now.
//
// This is exported because the single-post handler needs it too: an index
// lookup finds a post by URL regardless of visibility, so the handler has to
// apply the same rules the timeline does or a draft would be reachable by
// guessing its address.
func (f Filter) Matches(post PostMeta, now time.Time) bool {
	if post.Draft && !f.IncludeDrafts {
		return false
	}

	// A future publication date hides the post until its moment arrives. This
	// is checked at query time rather than by a scheduler, which means a
	// scheduled post appears on the first request after its timestamp with no
	// timer, no background job, and nothing to go wrong while the server is
	// asleep.
	if !f.IncludeScheduled && post.Published.After(now) {
		return false
	}

	if f.Tag != "" && !hasTag(post.Tags, f.Tag) {
		return false
	}

	return true
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// Cursor marks a position in the timeline for pagination.
//
// Pagination is cursor based rather than offset based, and that choice is worth
// defending because offsets are the obvious approach.
//
// With offsets, "give me posts 20 through 39" is evaluated against whatever the
// list looks like at that moment. Publish a post while somebody is scrolling
// and every subsequent page shifts by one: they see the post that was at
// position 19 again at position 20, and if a post is deleted instead, one
// silently disappears from their view entirely. Infinite scroll makes this
// likely rather than theoretical, because the reader is issuing requests over a
// span of minutes rather than all at once.
//
// A cursor says "give me what comes after this specific post" instead, which is
// unaffected by anything inserted or removed above it.
type Cursor struct {
	// Published is the timestamp of the last post already seen.
	Published time.Time

	// Slug disambiguates when several posts share a timestamp. It has to match
	// the tiebreaker used in sortPosts or pagination will skip entries.
	Slug string
}

// IsZero reports whether the cursor is unset, meaning start from the beginning.
func (c Cursor) IsZero() bool { return c.Published.IsZero() && c.Slug == "" }

// after reports whether a post falls strictly after the cursor in timeline
// order, which is what makes it eligible for the next page.
func (c Cursor) after(post PostMeta) bool {
	if c.IsZero() {
		return true
	}
	if post.Published.Equal(c.Published) {
		// Same timestamp, so fall back to the same tiebreaker the sort uses.
		return post.Slug > c.Slug
	}
	return post.Published.Before(c.Published)
}

// Page is one slice of the timeline.
type Page struct {
	Posts []PostMeta

	// Next is the cursor to request the following page. It is zero when this
	// page is the last one, which is how the template decides whether to
	// render the "next" link that infinite scroll hijacks.
	Next Cursor

	// HasMore reports whether any posts remain after this page.
	HasMore bool
}

// Query returns a page of posts matching the filter.
func (idx *Index) Query(filter Filter, cursor Cursor, limit int) Page {
	if limit <= 0 {
		limit = 20
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	now := time.Now().In(idx.loc)

	// Capacity is limit+1 because one extra post is collected to decide
	// HasMore. Asking "is there anything after this page" by fetching one more
	// item is cheaper and more accurate than counting the total, which would
	// mean walking the entire filtered set on every request.
	page := Page{Posts: make([]PostMeta, 0, limit)}

	for _, post := range idx.posts {
		if !filter.Matches(post, now) {
			continue
		}
		if !cursor.after(post) {
			continue
		}

		if len(page.Posts) == limit {
			// One more match exists, so there is another page. The post itself
			// is discarded; only its existence mattered.
			page.HasMore = true
			break
		}

		page.Posts = append(page.Posts, post)
	}

	if page.HasMore && len(page.Posts) > 0 {
		last := page.Posts[len(page.Posts)-1]
		page.Next = Cursor{Published: last.Published, Slug: last.Slug}
	}

	return page
}

// Lookup finds a post by its date and slug, which is what a URL carries.
func (idx *Index) Lookup(year int, month time.Month, day int, slug string) (PostMeta, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	i, exists := idx.bySlug[slugKey(year, month, day, slug)]
	if !exists {
		return PostMeta{}, false
	}
	return idx.posts[i], true
}

// Len reports how many posts are indexed, including drafts and scheduled ones.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.posts)
}

// Stats summarizes the index, for the health endpoint and the admin view.
type Stats struct {
	Total     int       `json:"total"`
	Published int       `json:"published"`
	Drafts    int       `json:"drafts"`
	Scheduled int       `json:"scheduled"`
	Tags      int       `json:"tags"`
	Newest    time.Time `json:"newest,omitempty"`
	Oldest    time.Time `json:"oldest,omitempty"`
}

// Stats computes a summary of the index.
func (idx *Index) Stats() Stats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	now := time.Now().In(idx.loc)
	tags := make(map[string]struct{})

	stats := Stats{Total: len(idx.posts)}

	for _, post := range idx.posts {
		switch {
		case post.Draft:
			stats.Drafts++
		case post.Published.After(now):
			stats.Scheduled++
		default:
			stats.Published++

			// Newest and oldest describe what a reader can actually see, so
			// drafts and scheduled posts are excluded. The slice is sorted
			// descending, so the first published post encountered is the
			// newest and the last is the oldest.
			if stats.Newest.IsZero() {
				stats.Newest = post.Published
			}
			stats.Oldest = post.Published
		}

		for _, tag := range post.Tags {
			tags[tag] = struct{}{}
		}
	}

	stats.Tags = len(tags)
	return stats
}

// TagCount pairs a tag with how many posts carry it.
type TagCount struct {
	Tag   string
	Count int
}

// Tags lists every tag in use, ordered by frequency and then alphabetically.
//
// The secondary alphabetical ordering matters: without it, tags with equal
// counts would appear in map iteration order, which Go randomizes, and the tag
// list would shuffle itself on every page load.
func (idx *Index) Tags(filter Filter) []TagCount {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	now := time.Now().In(idx.loc)
	counts := make(map[string]int)

	for _, post := range idx.posts {
		if !filter.Matches(post, now) {
			continue
		}
		for _, tag := range post.Tags {
			counts[tag]++
		}
	}

	result := make([]TagCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagCount{Tag: tag, Count: count})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Tag < result[j].Tag
	})

	return result
}

// ArchiveEntry counts the posts published in one month.
type ArchiveEntry struct {
	Year  int
	Month time.Month
	Count int
}

// Archive lists months that contain posts, newest first.
func (idx *Index) Archive(filter Filter) []ArchiveEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	now := time.Now().In(idx.loc)

	// The posts slice is already sorted newest first, so months come out in
	// the right order by appending as each new one is encountered. No sort is
	// needed.
	var entries []ArchiveEntry

	for _, post := range idx.posts {
		if !filter.Matches(post, now) {
			continue
		}
		year, month, _ := post.Published.Date()

		if n := len(entries); n > 0 && entries[n-1].Year == year && entries[n-1].Month == month {
			entries[n-1].Count++
			continue
		}
		entries = append(entries, ArchiveEntry{Year: year, Month: month, Count: 1})
	}

	return entries
}

// Search returns posts whose title, summary, or tags contain the query.
//
// This is a substring scan over metadata already in memory, which for a
// personal blog is both fast enough to be unnoticeable and simple enough to
// have no failure modes. Full-text search over bodies would mean either reading
// every file per query or holding every body in memory, and neither is worth it
// until somebody actually has the archive to justify it.
func (idx *Index) Search(query string, filter Filter, limit int) []PostMeta {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	now := time.Now().In(idx.loc)
	results := make([]PostMeta, 0, limit)

	for _, post := range idx.posts {
		if len(results) == limit {
			break
		}
		if !filter.Matches(post, now) {
			continue
		}
		if matchesQuery(post, query) {
			results = append(results, post)
		}
	}

	return results
}

// matchesQuery reports whether any searchable field contains the query.
func matchesQuery(post PostMeta, query string) bool {
	if strings.Contains(strings.ToLower(post.Title), query) {
		return true
	}
	if strings.Contains(strings.ToLower(post.Summary), query) {
		return true
	}
	for _, tag := range post.Tags {
		if strings.Contains(tag, query) {
			return true
		}
	}
	return false
}

// FromPost builds an index entry from a parsed post and its storage path.
func FromPost(post content.Post, storagePath string) (PostMeta, error) {
	// The path is parsed purely to reject anything that is not a real post
	// location before it reaches the index. The parsed components are not used
	// below, because the URL is derived from frontmatter instead; see the note
	// further down.
	if _, err := content.ParsePostPath(storagePath); err != nil {
		return PostMeta{}, err
	}

	// The URL is built from the published date in frontmatter rather than from
	// the path, so that a post whose date was corrected in its header resolves
	// at the address its metadata claims. The index and the router therefore
	// always agree, even when the file has not been moved to match yet.
	year, month, day := post.Published.Date()

	return PostMeta{
		Slug:      post.Slug,
		Title:     post.Title,
		Published: post.Published,
		Updated:   post.Updated,
		Draft:     post.Draft,
		Tags:      post.Tags,
		Summary:   post.Summary,
		Cover:     post.Cover,
		Path:      storagePath,
		URL:       content.PostPath{Year: year, Month: month, Day: day, Slug: post.Slug}.URL(),
	}, nil
}

// Build walks the store and constructs a complete index.
//
// Only frontmatter is parsed. The body of each file is read into memory to get
// at the header and then discarded, which is unavoidable without a streaming
// parser, but nothing is retained beyond the metadata.
func Build(store *content.Store) (*Index, []error) {
	idx := New(store.Location())

	var (
		posts    []PostMeta
		problems []error
	)

	err := store.Walk(func(storagePath string) error {
		post, err := store.Read(storagePath)
		if err != nil {
			// One malformed file must not stop the site from starting. The
			// problem is collected and reported so the operator can see it in
			// the logs, and the remaining posts still load.
			problems = append(problems, err)
			return nil
		}

		if err := post.Validate(); err != nil {
			problems = append(problems, err)
			return nil
		}

		meta, err := FromPost(post, storagePath)
		if err != nil {
			problems = append(problems, err)
			return nil
		}

		posts = append(posts, meta)
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}

	idx.Replace(posts)
	return idx, problems
}

// All returns a snapshot of every indexed post.
//
// The slice is a copy, so a caller iterating it cannot be tripped up by a
// concurrent write, and cannot mutate the index by accident. At a few hundred
// bytes per entry the copy is cheap next to the correctness it buys.
func (idx *Index) All() []PostMeta {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	posts := make([]PostMeta, len(idx.posts))
	copy(posts, idx.posts)
	return posts
}
