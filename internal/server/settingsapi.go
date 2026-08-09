package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"git.thebytes.net/roberts/broadside/internal/config"
)

// The settings API.
//
// Everything the Site Settings tab writes, over the same bearer token that
// writes posts. That is a deliberate equivalence: a token already lets its
// holder publish, edit, and delete, so withholding the site's title from it
// would be a lock on the window of a house with the front door open.
//
// What is not here is anything to do with credentials. The username, the
// password, and the tokens themselves live in auth.json and are reachable only
// from a signed-in browser session, because a token that can mint more tokens
// or change the password it was issued under cannot be revoked in any
// meaningful sense.

// handleAPIGetSettings returns the whole configuration.
func (s *Server) handleAPIGetSettings(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.cfg)
}

// handleAPIUpdateSettings applies a partial change.
//
// The body is decoded onto the configuration already in memory, so a client
// sending three fields changes three fields. The alternative, replacing the
// whole document, means every client has to send back settings it does not
// understand or silently reset them, and a phone app that predates a new
// setting would wipe it on every save.
func (s *Server) handleAPIUpdateSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that request body could not be read"})
		return
	}

	// The setup flag is never writable here. Turning it off puts the site back
	// on the first-run page, where anyone who reaches it can create an account,
	// so it is not something a request should be able to do by accident or
	// otherwise.
	cfg.SetupComplete = s.cfg.SetupComplete

	// An invalid date format is ignored rather than stored, so a typo cannot
	// leave every post on the site without a date.
	if !config.ValidDateFormat(strings.TrimSpace(cfg.DateFormat)) {
		cfg.DateFormat = s.cfg.DateFormat
	}

	// Font names are checked against the table the stylesheet is built from.
	// An unknown one falls back at render time regardless, so this is about not
	// storing a value the settings page would then show as a broken selection.
	cfg.Theme.SiteTitleFont = s.validFont(cfg.Theme.SiteTitleFont, s.cfg.Theme.SiteTitleFont, true)
	cfg.Theme.PostTitleFont = s.validFont(cfg.Theme.PostTitleFont, s.cfg.Theme.PostTitleFont, true)
	cfg.Theme.ContentFont = s.validFont(cfg.Theme.ContentFont, s.cfg.Theme.ContentFont, false)

	cfg.Title = strings.TrimSpace(cfg.Title)
	cfg.Slogan = strings.TrimSpace(cfg.Slogan)
	cfg.DisplayName = strings.TrimSpace(cfg.DisplayName)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Timezone = strings.TrimSpace(cfg.Timezone)
	cfg.Language = strings.TrimSpace(cfg.Language)

	if err := config.Save(s.store.Root(), cfg); err != nil {
		s.logger.Error("saving settings via API", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the settings could not be saved"})
		return
	}

	// Reload from disk rather than trusting the copy above, so the site runs
	// with exactly what was written, fallbacks and clamps included. The upload
	// limit and the password length are both bounded on the way to disk, and
	// the clamped value is the one that should take effect and the one the
	// client should be told about.
	saved, err := config.Load(s.store.Root())
	if err != nil {
		s.logger.Error("reloading settings after an API write", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the settings were saved but could not be reloaded"})
		return
	}

	s.applyConfig(saved)
	s.logger.Info("settings updated via API")

	s.writeJSON(w, http.StatusOK, saved)
}
