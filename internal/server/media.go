package server

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"git.thebytes.net/roberts/broadside/internal/content"
)

// Upload handling is where a publishing tool is most likely to be broken into,
// so the rules here are deliberately narrow.
//
// The governing principle is that nothing a client claims about a file is
// believed. Not the extension, not the Content-Type header, not the filename.
// The bytes are sniffed, checked against an allowlist, and stored under a name
// this server generates. A client's only influence on the stored name is the
// slug, which has already been through the same character filter as everything
// else.

// maxUploadMemory is how much of a multipart form is buffered in RAM before the
// rest spills to a temporary file on disk.
//
// This is not the size limit, which is configurable; it is the point at which
// Go stops holding the upload in memory. Eight megabytes keeps a small image
// entirely in RAM while making sure a large one never is, which is what stops
// three concurrent uploads of a stacked astrophotography frame from exhausting
// a small machine.
//
// The spill needs somewhere to write. See main for why that is set explicitly
// rather than left to the system temporary directory.
const maxUploadMemory = 8 << 20

// allowedUploadTypes maps a sniffed MIME type to the extension it is stored
// with.
//
// This is an allowlist, so a type nobody considered is refused rather than
// accepted by oversight. The extension comes from this table rather than from
// the uploaded filename, which is what stops "photo.jpg.php" from ever existing
// on disk.
//
// SVG is deliberately absent. It is XML that can carry script and event
// handlers, so a stored SVG served from this origin is a cross-site scripting
// vector. Supporting it safely means parsing and sanitizing it as XML against a
// strict allowlist, which is a real piece of work for a format nobody uploading
// a photograph needs.
var allowedUploadTypes = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/avif":      ".avif",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"audio/mpeg":      ".mp3",
	"audio/ogg":       ".ogg",
	"audio/wav":       ".wav",
	"application/pdf": ".pdf",
}

// mediaFile is an uploaded file as the media tab lists it.
type mediaFile struct {
	// Path is relative to the uploads directory.
	Path string

	// URL is the public address.
	URL string

	Name        string
	Size        int64
	DisplaySize string
	Modified    time.Time
	DisplayDate string

	// IsImage drives whether the list shows a thumbnail or a file row.
	IsImage bool
}

// listUploads walks the uploads directory, newest first.
func (s *Server) listUploads() ([]mediaFile, error) {
	var files []mediaFile

	err := fs.WalkDir(s.uploads.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry is skipped rather than failing the listing.
			// One bad file should not hide the rest of the library.
			return nil
		}
		if d.IsDir() || strings.HasPrefix(path.Base(p), ".") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		extension := strings.ToLower(path.Ext(p))
		files = append(files, mediaFile{
			Path:        p,
			URL:         "/uploads/" + p,
			Name:        path.Base(p),
			Size:        info.Size(),
			DisplaySize: humanSize(info.Size()),
			Modified:    info.ModTime(),
			DisplayDate: info.ModTime().Format("2 Jan 2006"),
			IsImage: extension == ".jpg" || extension == ".jpeg" || extension == ".png" ||
				extension == ".gif" || extension == ".webp" || extension == ".avif",
		})
		return nil
	})

	// Newest first, which is what somebody looking for the thing they just
	// uploaded expects.
	sort.Slice(files, func(i, j int) bool {
		return files[i].Modified.After(files[j].Modified)
	})

	return files, err
}

// handleUploadPost accepts a file.
//
// This serves both the media tab's form and the API, since the validation must
// be identical either way. Having two upload paths would mean two places for
// the rules to drift apart, and the weaker one would become the way in.
func (s *Server) handleUploadPost(w http.ResponseWriter, r *http.Request) {
	limit := s.cfg.MaxUploadBytes()

	// MaxBytesReader caps the request before anything is read, so an oversized
	// upload is rejected as it arrives rather than after it has already been
	// written somewhere.
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	if err := r.ParseMultipartForm(maxUploadMemory); err != nil {
		// Two very different failures arrive here and used to be reported
		// identically, which sent people looking for a size problem they did
		// not have. One is the body genuinely exceeding the limit. The other is
		// the spill to disk failing, which happened in the container because a
		// scratch image has no temporary directory, and reporting that as "too
		// large" is a straightforwardly misleading error.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.respondUpload(w, r, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("That file is larger than the %d MB limit. Raise it in Site Settings.", s.cfg.MaxUploadMB), "")
			return
		}

		s.logger.Error("reading upload", "error", err)
		s.respondUpload(w, r, http.StatusBadRequest,
			"That upload could not be read. Check the site folder is writable.", "")
		return
	}
	defer r.MultipartForm.RemoveAll()

	// A browser form posts a CSRF token; an API client authenticates with a
	// bearer token and has no session to protect.
	if session, ok := s.currentSession(r); ok {
		if !s.checkCSRF(r, session) {
			s.respondUpload(w, r, http.StatusForbidden, "That request could not be verified.", "")
			return
		}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		s.respondUpload(w, r, http.StatusBadRequest, "No file was included.", "")
		return
	}
	defer file.Close()

	url, err := s.storeUpload(file, header.Filename)
	if err != nil {
		s.logger.Error("storing upload", "filename", header.Filename, "error", err)
		s.respondUpload(w, r, http.StatusBadRequest, err.Error(), "")
		return
	}

	s.logger.Info("upload stored", "url", url)
	s.respondUpload(w, r, http.StatusCreated, "", url)
}

// storeUpload validates and writes a file, returning its public URL.
func (s *Server) storeUpload(file io.Reader, originalName string) (string, error) {
	// Only the first 512 bytes are needed to identify a type, which is what
	// http.DetectContentType examines.
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", fmt.Errorf("that file could not be read")
	}
	head = head[:n]

	// The sniffed type decides everything. The uploaded filename's extension
	// and the browser's Content-Type header are both attacker-controlled and
	// are never consulted.
	detected := strings.SplitN(http.DetectContentType(head), ";", 2)[0]

	extension, permitted := allowedUploadTypes[detected]
	if !permitted {
		return "", fmt.Errorf("files of type %s are not accepted", detected)
	}

	// The stored name comes from the original only through the slugifier, so
	// path separators, dots, and control characters cannot survive into it.
	base := strings.TrimSuffix(originalName, path.Ext(originalName))
	slug := content.SlugifyWithFallback(base, "file")

	now := time.Now().In(s.store.Location())
	dayDir := now.Format("2006/01/02")

	sequence, err := content.NextUploadSequence(s.uploads, dayDir)
	if err != nil {
		return "", fmt.Errorf("that file could not be filed")
	}

	storedPath := fmt.Sprintf("%s/%02d-%s%s", dayDir, sequence, slug, extension)

	// Written through the uploads root, so the destination cannot escape the
	// uploads directory even if the name construction above were ever changed
	// carelessly.
	out, err := s.uploads.Create(storedPath)
	if err != nil {
		return "", fmt.Errorf("that file could not be written")
	}
	defer out.Close()

	// The sniffed head has already been consumed from the reader, so it is
	// written back before the remainder.
	if _, err := out.Write(head); err != nil {
		return "", fmt.Errorf("that file could not be written")
	}
	if _, err := io.Copy(out, file); err != nil {
		return "", fmt.Errorf("that file could not be written")
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("that file could not be written")
	}

	return "/uploads/" + storedPath, nil
}

// handleMediaDelete removes an uploaded file.
func (s *Server) handleMediaDelete(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	target := r.FormValue("path")

	// Removed through the uploads root, so a crafted path cannot delete
	// anything outside the uploads directory.
	if err := s.uploads.Remove(target); err != nil {
		s.logger.Error("deleting upload", "path", target, "error", err)
		http.Redirect(w, r, "/admin/media?notice=That+file+could+not+be+deleted", http.StatusSeeOther)
		return
	}

	s.logger.Info("upload deleted", "path", target)
	http.Redirect(w, r, "/admin/media?notice=File+deleted", http.StatusSeeOther)
}

// respondUpload answers an upload in the form the caller expects.
//
// A browser form gets a redirect back to the media tab; an API client gets
// JSON. The two are distinguished by the Accept header, so one handler serves
// both without either seeing a response it cannot use.
func (s *Server) respondUpload(w http.ResponseWriter, r *http.Request, status int, message, url string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") || s.bearerToken(r) != "" {
		if message != "" {
			s.writeJSON(w, status, map[string]string{"error": message})
			return
		}
		s.writeJSON(w, status, map[string]string{"url": url})
		return
	}

	if message != "" {
		http.Redirect(w, r, "/admin/media?notice="+urlEncode(message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/media?notice=File+uploaded", http.StatusSeeOther)
}

// humanSize renders a byte count for display.
func humanSize(size int64) string {
	const unit = 1000 // Decimal, to match what a file manager shows.

	if size < unit {
		return fmt.Sprintf("%d B", size)
	}

	div, exponent := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "kMGT"[exponent])
}

// urlEncode escapes a value for use in a query string.
func urlEncode(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('+')
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}
