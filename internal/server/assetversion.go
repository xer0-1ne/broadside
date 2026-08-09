package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
)

// assetVersion is a short hash of everything under static, computed once at
// startup and appended to the stylesheet and script URLs as a query string.
//
// Without it, the one-hour cache lifetime on those files means an upgraded
// Broadside serves its new HTML alongside the previous stylesheet for up to an
// hour, which shows up as a site that is subtly broken and then fixes itself.
// Cache-busting on content rather than on a release number also means a rebuilt
// stylesheet is picked up even between versions.
//
// The hash covers file contents rather than modification times, because an
// embedded filesystem has no meaningful timestamps and two builds of the same
// source should produce the same URL.
var assetVersion = computeAssetVersion()

func computeAssetVersion() string {
	digest := sha256.New()

	// Walked in lexical order, which fs.WalkDir guarantees, so the hash is
	// stable across builds and machines.
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		// The name is hashed alongside the contents, so renaming a file changes
		// the version even when its bytes do not.
		digest.Write([]byte(path))

		data, err := fs.ReadFile(staticFS, path)
		if err != nil {
			return nil
		}
		digest.Write(data)
		return nil
	})
	if err != nil {
		// A hash that cannot be computed falls back to a constant rather than
		// failing to start. The result is the previous behaviour, which is
		// stale assets for an hour, not a site that will not boot.
		return "0"
	}

	// Eight hex characters is thirty-two bits, which is far more than enough to
	// distinguish one build from the next, and short enough to keep the URL
	// readable in a network log.
	return hex.EncodeToString(digest.Sum(nil))[:8]
}
