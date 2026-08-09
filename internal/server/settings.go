package server

import (
	"net/http"
	"strconv"
	"strings"

	"git.thebytes.net/roberts/broadside/internal/auth"
	"git.thebytes.net/roberts/broadside/internal/config"
)

// The settings tab is split into four forms rather than one.
//
// A single form would mean the password fields ride along with every save, so
// changing a color would post the password boxes too, and a browser's password
// manager would offer to update the saved credential every time. Separating
// them keeps each submission about one thing, which is also what lets the
// account form require the current password without demanding it for a change
// of slogan.

// handleAdminSettings renders the settings tab.
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	page, admin := s.newAdminPage(r, tabSettings)
	page.Heading = "Site Settings"

	admin.Settings = s.cfg
	admin.Username = s.authStore.Username()
	admin.Email = s.authStore.Email()

	admin.SiteTitleFonts = s.fontChoices(true)
	admin.PostTitleFonts = s.fontChoices(true)
	admin.ContentFonts = s.fontChoices(false)
	admin.UploadedFonts = s.listUploadedFonts()

	admin.AllPlatforms = Platforms()
	admin.CustomIcons = s.listIcons()
	admin.DateExamples = config.DateFormatExamples()
	admin.DefaultFooter = config.DefaultFooterText
	admin.MaxUploadCeiling = config.MaxUploadCeilingMB
	admin.MinPasswordFloor = config.MinPasswordFloor

	// Blank rows are added so the form always offers somewhere to put another
	// link. The Add button below is a convenience on top of this; without the
	// spare rows a reader with JavaScript disabled could never add one.
	admin.SocialEntries = append([]config.SocialLink{}, s.cfg.Social...)
	for i := 0; i < 3; i++ {
		admin.SocialEntries = append(admin.SocialEntries, config.SocialLink{})
	}

	s.renderPage(w, r, "admin-settings.html", page)
}

// fontChoices builds a dropdown list, uploaded typefaces first.
//
// Uploaded fonts lead because somebody who has just added their own is looking
// for it, and burying it under seven built-ins that were already there is the
// wrong order for that moment.
//
// includeDisplay admits faces that are fine for a title and poor for body text.
// The distinction is not tidiness: a handwritten face carries a masthead
// beautifully and is genuinely tiring across a full post, and offering it for
// body text mostly serves to let somebody make their own site unreadable.
func (s *Server) fontChoices(includeDisplay bool) []FontOption {
	var options []FontOption

	for _, font := range s.listUploadedFonts() {
		options = append(options, FontOption{
			Value:    font.Value,
			Label:    font.Label,
			Note:     "Uploaded",
			Stack:    quoteFamily(font.Family) + ", sans-serif",
			BodySafe: true,
		})
	}

	for _, option := range fontOptions {
		if !includeDisplay && !option.BodySafe {
			continue
		}
		options = append(options, option)
	}

	return options
}

// handleAdminSettingsSave applies the site form.
func (s *Server) handleAdminSettingsSave(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r, session) {
		// A missing or wrong token means the request did not come from this
		// site's own form, so it is refused outright rather than partly
		// applied.
		http.Error(w, "That request could not be verified. Reload the page and try again.", http.StatusForbidden)
		return
	}

	cfg := s.cfg

	cfg.Title = strings.TrimSpace(r.FormValue("title"))
	cfg.Slogan = strings.TrimSpace(r.FormValue("slogan"))
	cfg.DisplayName = strings.TrimSpace(r.FormValue("display_name"))
	cfg.BaseURL = strings.TrimSpace(r.FormValue("base_url"))
	cfg.Image = strings.TrimSpace(r.FormValue("image"))
	cfg.Favicon = strings.TrimSpace(r.FormValue("favicon"))
	cfg.Timezone = strings.TrimSpace(r.FormValue("timezone"))
	cfg.Language = strings.TrimSpace(r.FormValue("language"))
	cfg.FooterText = strings.TrimSpace(r.FormValue("footer_text"))

	if n, err := strconv.Atoi(r.FormValue("posts_per_page")); err == nil {
		cfg.PostsPerPage = n
	}
	if n, err := strconv.Atoi(r.FormValue("max_upload_mb")); err == nil {
		cfg.MaxUploadMB = n
	}
	if n, err := strconv.Atoi(r.FormValue("min_password_length")); err == nil {
		cfg.MinPasswordLength = n
	}

	// An invalid date format is ignored rather than stored, so a typo cannot
	// leave every post on the site without a date. applyFallbacks would catch
	// it anyway, but silently replacing the author's input with the default is
	// worse than keeping what already worked.
	if format := strings.TrimSpace(r.FormValue("date_format")); config.ValidDateFormat(format) {
		cfg.DateFormat = format
	}

	cfg.Theme.Background = r.FormValue("color_background")
	cfg.Theme.Surface = r.FormValue("color_surface")
	cfg.Theme.Text = r.FormValue("color_text")
	cfg.Theme.Muted = r.FormValue("color_muted")
	cfg.Theme.Accent = r.FormValue("color_accent")
	cfg.Theme.Border = r.FormValue("color_border")

	// Font values come from dropdowns, but the request is only a form post and
	// could carry anything. Unknown names are dropped rather than stored, so a
	// crafted request cannot put arbitrary text into the stylesheet route.
	cfg.Theme.SiteTitleFont = s.validFont(r.FormValue("site_title_font"), cfg.Theme.SiteTitleFont, true)
	cfg.Theme.PostTitleFont = s.validFont(r.FormValue("post_title_font"), cfg.Theme.PostTitleFont, true)
	cfg.Theme.ContentFont = s.validFont(r.FormValue("content_font"), cfg.Theme.ContentFont, false)

	cfg.Social = s.parseSocialRows(r)

	if err := config.Save(s.store.Root(), cfg); err != nil {
		s.logger.Error("saving settings", "error", err)
		http.Error(w, "The settings could not be saved.", http.StatusInternalServerError)
		return
	}

	// Reload from disk rather than trusting the in-memory copy, so the site
	// runs with exactly the values that were written, fallbacks included. That
	// matters most for the two numbers above, where a value out of range is
	// clamped on the way to disk and the clamped one is what should take
	// effect.
	if saved, err := config.Load(s.store.Root()); err == nil {
		s.applyConfig(saved)
	} else {
		s.logger.Error("reloading settings", "error", err)
	}

	s.logger.Info("settings saved", "username", session.Username)
	s.redirectSettings(w, r, "Settings saved")
}

// parseSocialRows reads the repeating social link fields.
//
// The fields arrive as parallel arrays, one entry per row. A row with no value
// is dropped, which is also how a row is deleted: clear it and save.
func (s *Server) parseSocialRows(r *http.Request) []config.SocialLink {
	platforms := r.Form["social_platform"]
	values := r.Form["social_value"]
	labels := r.Form["social_label"]
	icons := r.Form["social_icon"]

	var links []config.SocialLink

	for i := range platforms {
		if i >= len(values) {
			break
		}

		platform := strings.TrimSpace(platforms[i])
		value := strings.TrimSpace(values[i])
		if platform == "" || value == "" {
			continue
		}

		link := config.SocialLink{Platform: platform, URL: value}

		if i < len(labels) {
			link.Label = strings.TrimSpace(labels[i])
		}

		if platform == config.CustomPlatform {
			// A custom row needs both a label and an icon to render as
			// anything. Without a label there is nothing for a screen reader to
			// announce, so the row is skipped rather than shown as a blank.
			if i < len(icons) {
				link.Icon = strings.TrimSpace(icons[i])
			}
			if link.Label == "" || link.Icon == "" {
				continue
			}
		} else if _, known := platformsByValue[platform]; !known {
			continue
		}

		links = append(links, link)
	}

	return links
}

// validFont returns the submitted font if it is one that exists, and the
// current value otherwise.
func (s *Server) validFont(submitted, current string, allowDisplay bool) string {
	submitted = strings.TrimSpace(submitted)
	if submitted == "" {
		return current
	}

	if strings.HasPrefix(submitted, config.UploadedFontPrefix) {
		// An uploaded font is only valid while its file is still present, so a
		// stale value from a page left open does not leave the site pointing at
		// a font that was deleted in the meantime.
		name := strings.TrimPrefix(submitted, config.UploadedFontPrefix)
		for _, font := range s.listUploadedFonts() {
			if font.File == name {
				return submitted
			}
		}
		return current
	}

	option, known := fontsByValue[submitted]
	if !known {
		return current
	}
	if !allowDisplay && !option.BodySafe {
		return current
	}
	return submitted
}

// handleAdminAccountSave updates the username and email.
func (s *Server) handleAdminAccountSave(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))

	if err := s.authStore.SetAccount(username, email); err != nil {
		s.redirectSettings(w, r, capitalize(strings.TrimPrefix(err.Error(), "auth: ")))
		return
	}

	s.logger.Info("account details updated", "username", username)
	s.redirectSettings(w, r, "Account updated")
}

// handleAdminPasswordChange rotates the password.
func (s *Server) handleAdminPasswordChange(w http.ResponseWriter, r *http.Request) {
	session, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r, session) {
		http.Error(w, "That request could not be verified.", http.StatusForbidden)
		return
	}

	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if next != confirm {
		s.redirectSettings(w, r, "Those two passwords do not match")
		return
	}

	// The current session is kept so the author is not signed out of the page
	// they are standing on. Every other session is dropped, which is the
	// recovery path when one is believed to be compromised.
	if err := s.authStore.ChangePassword(current, next, session.ID); err != nil {
		if err == auth.ErrBadCredentials {
			s.logger.Warn("failed password change", "username", session.Username, "remote", s.clientIP(r))
			s.redirectSettings(w, r, "That current password is not right")
			return
		}
		s.redirectSettings(w, r, capitalize(strings.TrimPrefix(err.Error(), "auth: ")))
		return
	}

	s.logger.Info("password changed", "username", session.Username)
	s.redirectSettings(w, r, "Password changed. Any other sessions have been signed out.")
}

// redirectSettings returns to the settings tab with a message.
func (s *Server) redirectSettings(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/admin/settings?notice="+urlEncode(notice), http.StatusSeeOther)
}

// quoteFamily wraps a CSS family name in quotes when it contains a space.
//
// An unquoted family name with a space in it is a syntax error that silently
// drops the declaration, so an uploaded font called "My Font" would simply not
// apply, with nothing to indicate why.
func quoteFamily(family string) string {
	if strings.ContainsAny(family, " \t") {
		return `"` + strings.ReplaceAll(family, `"`, "") + `"`
	}
	return family
}
