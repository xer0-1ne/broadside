package config

import (
	"strings"
	"testing"
	"time"
)

// sample is a deliberately awkward date: a single-digit day, so padded and
// unpadded forms are visibly different, and a month whose abbreviation differs
// from its full name.
var sample = time.Date(2026, time.August, 6, 14, 30, 0, 0, time.UTC)

func TestFormatDate(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		// The examples from the specification, which matter most because they
		// are what somebody will try first.
		{"D, M, Y", "6, August, 2026"},
		{"D m Y", "6 Aug 2026"},
		{"y d -- M.", "26 06 -- August."},

		{"M D, Y", "August 6, 2026"},
		{"d/m/Y", "06/Aug/2026"},
		{"Y.m.d", "2026.Aug.06"},
		{"D-M-y", "6-August-26"},
		{"y", "26"},
		{"Y", "2026"},

		// Every permitted divider, to confirm none is dropped.
		{"D M Y", "6 August 2026"},
		{"D-M-Y", "6-August-2026"},
		{"D,M,Y", "6,August,2026"},
		{"D|M|Y", "6|August|2026"},
		{"D_M_Y", "6_August_2026"},
		{"D:M:Y", "6:August:2026"},
		{"D;M;Y", "6;August;2026"},
		{"D.M.Y", "6.August.2026"},
		{"D/M/Y", "6/August/2026"},
		{`D\M\Y`, `6\August\2026`},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			if got := FormatDate(sample, tc.format); got != tc.want {
				t.Errorf("FormatDate(%q) = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
}

// TestMarkupInAFormatIsRefused is the property that matters most for a value
// the author types and every post on the site then renders.
//
// A format containing anything outside the token and divider sets is rejected
// whole and replaced with the default, rather than having the offending
// characters stripped and the remainder used. Rejecting outright is the safer
// of the two: partial stripping means reasoning about exactly what survives,
// and it takes only one oversight there for a fragment of markup to reach every
// byline.
func TestMarkupInAFormatIsRefused(t *testing.T) {
	wantDefault := FormatDate(sample, DefaultDateFormat)

	hostile := []string{
		"D <b>M</b> Y",
		"D <img src=x onerror=alert(1)> Y",
		"D &amp; Y",
		"D <script>M</script> Y",
	}

	for _, format := range hostile {
		got := FormatDate(sample, format)

		if got != wantDefault {
			t.Errorf("FormatDate(%q) = %q, want it refused back to the default %q", format, got, wantDefault)
		}
		if strings.ContainsAny(got, "<>&") {
			t.Errorf("FormatDate(%q) = %q, which still carries markup characters", format, got)
		}
	}
}

// TestFormatDateFallsBackWhenUseless covers formats that would otherwise render
// a post with no visible date, which reads as a rendering bug rather than as a
// setting.
func TestFormatDateFallsBackWhenUseless(t *testing.T) {
	wantDefault := FormatDate(sample, DefaultDateFormat)

	for _, format := range []string{"", "   ", "---", "!!!", "@#$", " , . ; "} {
		if got := FormatDate(sample, format); got != wantDefault {
			t.Errorf("FormatDate(%q) = %q, want the default %q", format, got, wantDefault)
		}
	}
}

func TestValidDateFormat(t *testing.T) {
	valid := []string{"M D, Y", "D m Y", "d/m/Y", "y d -- M.", "Y", "d"}
	for _, format := range valid {
		if !ValidDateFormat(format) {
			t.Errorf("ValidDateFormat(%q) = false, want true", format)
		}
	}

	invalid := []struct {
		format string
		reason string
	}{
		{"", "empty"},
		{"   ", "only whitespace"},
		{"- , -", "no token at all"},
		{"D <b>M</b>", "contains characters outside the set"},
		{"D H:i", "H and i are not tokens"},
	}
	for _, tc := range invalid {
		if ValidDateFormat(tc.format) {
			t.Errorf("ValidDateFormat(%q) = true, want false because %s", tc.format, tc.reason)
		}
	}
}

// TestPaddingIsRespected covers the distinction most easily got wrong, since
// both day tokens produce the same output for two-digit days.
func TestPaddingIsRespected(t *testing.T) {
	singleDigit := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	doubleDigit := time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)

	if got := FormatDate(singleDigit, "D"); got != "6" {
		t.Errorf("D on the 6th = %q, want %q", got, "6")
	}
	if got := FormatDate(singleDigit, "d"); got != "06" {
		t.Errorf("d on the 6th = %q, want %q", got, "06")
	}

	// On a two-digit day the two tokens agree, which is why the single-digit
	// case above is the one that proves anything.
	for _, token := range []string{"D", "d"} {
		if got := FormatDate(doubleDigit, token); got != "16" {
			t.Errorf("%s on the 16th = %q, want %q", token, got, "16")
		}
	}
}

// TestRepeatedTokensAreAcceptedButUseless documents a trap rather than a bug.
//
// "YYYY-MM-DD" is what somebody arriving from almost any other date library
// will type, and every character in it is a valid token or divider, so it is
// accepted. It renders as nonsense, because each letter expands independently.
// Rejecting it would mean inventing a rule about repetition the language does
// not otherwise have, so instead the settings form shows a live preview and the
// mistake is visible while it is being made.
func TestRepeatedTokensAreAcceptedButUseless(t *testing.T) {
	if !ValidDateFormat("YYYY-MM-DD") {
		t.Error("expected the format to be accepted, since every character in it is valid")
	}

	got := FormatDate(sample, "YYYY-MM-DD")
	if got == "2026-08-06" {
		t.Error("this is not a date pattern in this language and must not silently behave like one")
	}
	if got != "2026202620262026-AugustAugust-66" {
		t.Errorf("got %q; the expansion changed, so the preview note may need revisiting", got)
	}
}

func TestDateFormatExamplesAllRender(t *testing.T) {
	examples := DateFormatExamples()
	if len(examples) == 0 {
		t.Fatal("no examples were produced, so the settings help text would be empty")
	}

	for _, example := range examples {
		if example.Result == "" {
			t.Errorf("example %q produced no output", example.Format)
		}
		if !ValidDateFormat(example.Format) {
			t.Errorf("example %q is not itself a valid format", example.Format)
		}
	}
}
