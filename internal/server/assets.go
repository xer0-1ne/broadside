package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/config"
	"git.thebytes.net/roberts/broadside/internal/content"
)

// Uploaded fonts and custom social icons live under core/ rather than in the
// uploads folder.
//
// They are configuration rather than content. Nothing in a post links to them,
// they are not part of the media library, and somebody backing up their writing
// should not find a typeface in among it. Keeping them under core also means
// the two mounted volumes stay meaningful: core is settings, uploads and posts
// are what you wrote.

const (
	// fontsDir holds uploaded typefaces, relative to the site root.
	fontsDir = "core/fonts"

	// iconsDir holds uploaded social icons.
	iconsDir = "core/icons"

	// maxFontSize caps a single typeface.
	//
	// Two megabytes is generous. A Latin-subset woff2 is tens of kilobytes, and
	// even a full-coverage variable font rarely passes one megabyte, so this
	// only stops somebody uploading something that was never a font.
	maxFontSize = 2 << 20

	// maxIconSize caps a social icon. An icon is drawn at twenty pixels, so
	// anything approaching this is already far larger than it needs to be.
	maxIconSize = 512 << 10
)

// fontSignatures maps the magic bytes at the start of a font file to its
// extension.
//
// http.DetectContentType is no help here: it does not recognize any font
// format and reports them all as application/octet-stream, which would mean
// either rejecting every font or accepting every binary. These four signatures
// are what the formats actually begin with.
//
// woff2 is the only one worth uploading in practice, and the others are
// accepted because somebody will have a .ttf to hand and telling them to go
// and convert it is unhelpful when the browser will render it fine.
var fontSignatures = []struct {
	magic     []byte
	extension string
}{
	{[]byte("wOF2"), ".woff2"},
	{[]byte("wOFF"), ".woff"},
	{[]byte("OTTO"), ".otf"},
	{[]byte{0x00, 0x01, 0x00, 0x00}, ".ttf"},
	{[]byte("true"), ".ttf"}, // An older TrueType variant.
}

// iconTypes maps a sniffed MIME type to the extension a social icon is stored
// with.
//
// SVG is accepted here even though the media library refuses it, and the
// difference is worth explaining. The danger with SVG is that it is XML which
// can carry script, so serving one as a document on this origin is a stored
// scripting vector. These are only ever rendered inside an img element, where
// script is inert, and they are served by a handler that sends a content
// security policy blocking script even if somebody navigates to the file
// directly. Both of those together are what make it safe; either alone would
// not be.
var iconTypes = map[string]string{
	"image/png":     ".png",
	"image/webp":    ".webp",
	"image/gif":     ".gif",
	"image/svg+xml": ".svg",
	"text/xml":      ".svg", // What sniffing usually reports for an SVG.
	"text/plain":    ".svg", // And what it reports for a small one.
}

// UploadedFont is a typeface the operator has added.
type UploadedFont struct {
	// File is the stored filename, which is also the value written to the
	// config as "upload:<file>".
	File string

	// Family is the CSS family name, derived from the filename.
	Family string

	// Label is what the settings dropdown shows.
	Label string

	// Value is what the form field submits.
	Value string
}

// listUploadedFonts returns the typefaces in core/fonts.
func (s *Server) listUploadedFonts() []UploadedFont {
	entries, err := s.store.Root().ReadDir(fontsDir)
	if err != nil {
		// A missing directory means none have been uploaded, which is the
		// normal case rather than a failure.
		return nil
	}

	var fonts []UploadedFont
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		name := entry.Name()
		family := fontFamilyFor(name)

		fonts = append(fonts, UploadedFont{
			File:   name,
			Family: family,
			Label:  family,
			Value:  config.UploadedFontPrefix + name,
		})
	}

	sort.Slice(fonts, func(i, j int) bool { return fonts[i].Label < fonts[j].Label })
	return fonts
}

// fontFamilyFor derives a CSS family name from a stored filename.
//
// The name is reconstructed from the slug rather than read out of the font
// file's own name table. Parsing the name table means understanding the
// container format for all four of the accepted types, and the result is only
// used as a label and a CSS family string, both of which the filename serves
// perfectly well.
func fontFamilyFor(filename string) string {
	base := strings.TrimSuffix(filename, path.Ext(filename))

	words := strings.Split(base, "-")
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

// handleFontUpload stores an uploaded typeface.
func (s *Server) handleFontUpload(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFontSize)
	if err := r.ParseMultipartForm(maxFontSize); err != nil {
		s.redirectSettings(w, r, "That font is too large")
		return
	}
	defer r.MultipartForm.RemoveAll()

	if !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	file, header, err := r.FormFile("font")
	if err != nil {
		s.redirectSettings(w, r, "No font was included")
		return
	}
	defer file.Close()

	if err := s.storeFont(file, header.Filename); err != nil {
		s.redirectSettings(w, r, err.Error())
		return
	}

	s.logger.Info("font uploaded", "filename", header.Filename)
	s.redirectSettings(w, r, "Font uploaded")
}

// storeFont validates and writes a typeface.
func (s *Server) storeFont(file io.Reader, originalName string) error {
	head := make([]byte, 8)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return fmt.Errorf("that file could not be read")
	}
	head = head[:n]

	// The extension comes from the file's own magic bytes, never from the name
	// it arrived with. That is what stops something that is not a font being
	// stored where the stylesheet will link to it.
	extension := ""
	for _, signature := range fontSignatures {
		if bytes.HasPrefix(head, signature.magic) {
			extension = signature.extension
			break
		}
	}
	if extension == "" {
		return fmt.Errorf("that is not a font file (woff2, woff, ttf, or otf)")
	}

	base := strings.TrimSuffix(originalName, path.Ext(originalName))
	slug := content.SlugifyWithFallback(base, "font")

	root, err := s.store.Root().Sub(fontsDir)
	if err != nil {
		if mkErr := s.store.Root().MkdirAll(fontsDir); mkErr != nil {
			return fmt.Errorf("the fonts folder could not be created")
		}
		if root, err = s.store.Root().Sub(fontsDir); err != nil {
			return fmt.Errorf("the fonts folder could not be opened")
		}
	}
	defer root.Close()

	out, err := root.Create(slug + extension)
	if err != nil {
		return fmt.Errorf("that font could not be written")
	}
	defer out.Close()

	if _, err := out.Write(head); err != nil {
		return fmt.Errorf("that font could not be written")
	}
	if _, err := io.Copy(out, file); err != nil {
		return fmt.Errorf("that font could not be written")
	}
	return out.Sync()
}

// handleFontDelete removes an uploaded typeface.
func (s *Server) handleFontDelete(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	root, err := s.store.Root().Sub(fontsDir)
	if err != nil {
		s.redirectSettings(w, r, "That font could not be removed")
		return
	}
	defer root.Close()

	name := r.FormValue("file")
	if err := root.Remove(name); err != nil {
		s.redirectSettings(w, r, "That font could not be removed")
		return
	}

	// A font still selected in the theme would leave the site referencing a
	// file that no longer exists, so any setting pointing at it falls back to
	// the default.
	cfg := s.cfg
	removed := config.UploadedFontPrefix + name
	changed := false
	for _, field := range []*string{&cfg.Theme.SiteTitleFont, &cfg.Theme.PostTitleFont, &cfg.Theme.ContentFont} {
		if *field == removed {
			*field = config.LightTheme.ContentFont
			changed = true
		}
	}
	if changed {
		if err := config.Save(s.store.Root(), cfg); err == nil {
			s.SetConfig(cfg)
		}
	}

	s.logger.Info("font removed", "file", name)
	s.redirectSettings(w, r, "Font removed")
}

// handleIconUpload stores a custom social icon.
func (s *Server) handleIconUpload(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIconSize)
	if err := r.ParseMultipartForm(maxIconSize); err != nil {
		s.redirectSettings(w, r, "That icon is too large")
		return
	}
	defer r.MultipartForm.RemoveAll()

	if !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		s.redirectSettings(w, r, "No icon was included")
		return
	}
	defer file.Close()

	stored, err := s.storeIcon(file, header.Filename)
	if err != nil {
		s.redirectSettings(w, r, err.Error())
		return
	}

	s.logger.Info("icon uploaded", "path", stored)
	s.redirectSettings(w, r, "Icon uploaded. Choose it on a Custom social row.")
}

// storeIcon validates and writes a social icon, returning its public path.
func (s *Server) storeIcon(file io.Reader, originalName string) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", fmt.Errorf("that file could not be read")
	}
	head = head[:n]

	detected := strings.SplitN(http.DetectContentType(head), ";", 2)[0]

	extension, permitted := iconTypes[detected]
	if !permitted {
		return "", fmt.Errorf("icons must be PNG, WebP, GIF, or SVG")
	}

	// Sniffing reports a small SVG as text, so the claim is confirmed by
	// looking for the root element before it is stored as one. Otherwise any
	// text file at all would be filed as an icon.
	if extension == ".svg" && !bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return "", fmt.Errorf("icons must be PNG, WebP, GIF, or SVG")
	}

	base := strings.TrimSuffix(originalName, path.Ext(originalName))
	slug := content.SlugifyWithFallback(base, "icon")

	if err := s.store.Root().MkdirAll(iconsDir); err != nil {
		return "", fmt.Errorf("the icons folder could not be created")
	}
	root, err := s.store.Root().Sub(iconsDir)
	if err != nil {
		return "", fmt.Errorf("the icons folder could not be opened")
	}
	defer root.Close()

	name := slug + extension
	out, err := root.Create(name)
	if err != nil {
		return "", fmt.Errorf("that icon could not be written")
	}
	defer out.Close()

	if _, err := out.Write(head); err != nil {
		return "", fmt.Errorf("that icon could not be written")
	}
	if _, err := io.Copy(out, file); err != nil {
		return "", fmt.Errorf("that icon could not be written")
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("that icon could not be written")
	}

	return "/icons/" + name, nil
}

// listIcons returns the uploaded social icons.
func (s *Server) listIcons() []string {
	entries, err := s.store.Root().ReadDir(iconsDir)
	if err != nil {
		return nil
	}

	var icons []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		icons = append(icons, "/icons/"+entry.Name())
	}
	sort.Strings(icons)
	return icons
}

// handleServeFont serves an uploaded typeface.
func (s *Server) handleServeFont(w http.ResponseWriter, r *http.Request) {
	s.serveFromCore(w, r, fontsDir, r.PathValue("file"), map[string]string{
		".woff2": "font/woff2",
		".woff":  "font/woff",
		".ttf":   "font/ttf",
		".otf":   "font/otf",
	})
}

// handleServeIcon serves an uploaded social icon.
//
// The content security policy here is the reason an SVG icon is safe to accept
// at all. Even if somebody navigates directly to the file rather than seeing it
// inside an img element, this response permits no script, no plugins, and no
// subresources of any kind, so an SVG carrying a script tag does nothing.
func (s *Server) handleServeIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")

	s.serveFromCore(w, r, iconsDir, r.PathValue("file"), map[string]string{
		".png":  "image/png",
		".webp": "image/webp",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
	})
}

// serveFromCore reads a file from a directory under core and serves it with an
// explicit content type.
func (s *Server) serveFromCore(w http.ResponseWriter, r *http.Request, dir, name string, types map[string]string) {
	root, err := s.store.Root().Sub(dir)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	defer root.Close()

	// Read through a root confined to the directory itself, so a crafted name
	// cannot reach a sibling under core such as the credentials file. This is
	// the same reasoning as the uploads handler, and the same mistake would be
	// available here if the path were joined first.
	data, err := root.ReadFile(name)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}

	contentType, known := types[strings.ToLower(path.Ext(name))]
	if !known {
		// A file with an unexpected extension is refused rather than guessed
		// at. Everything in these directories was written by the upload
		// handlers, so an unknown extension means something else put it there.
		s.renderNotFound(w, r)
		return
	}

	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("X-Content-Type-Options", "nosniff")

	// These change only when the operator replaces them, and a replacement
	// keeps the same name, so the lifetime is moderate rather than immutable.
	header.Set("Cache-Control", "public, max-age=3600")

	// A zero time suppresses Last-Modified, so revalidation is governed by the
	// cache lifetime set above rather than by a timestamp that changes whenever
	// the site folder is copied.
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}
