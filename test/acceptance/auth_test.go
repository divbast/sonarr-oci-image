// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// TestHealthyUnderEveryAuthMethod runs the image under each authentication
// method, with authentication required, and checks the probe stays
// healthy: it sends the API key, which every method accepts, so neither
// half needs a session. Each method must be the one in force, as the
// status gives it, or the probe would only have been run under None four
// times. Oidc is left out: in force, it fetches its provider's metadata
// over the network, which no container here reaches.
func TestHealthyUnderEveryAuthMethod(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	for _, tc := range []struct{ method, inForce string }{
		{method: "None", inForce: "none"},
		// Sonarr has retired Basic, and runs Forms in its place
		{method: "Basic", inForce: "forms"},
		{method: "Forms", inForce: "forms"},
		{method: "External", inForce: "external"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			s := serveSonarr(t, e, image, pingPath, keyed(map[string]string{
				"SONARR__AUTH__METHOD":   tc.method,
				"SONARR__AUTH__REQUIRED": "Enabled",
			}), configVolume(t, e))

			status, ok := callAPI[systemStatus](t, s, "GET /api/v5/system/status answers 200", http.StatusOK,
				http.MethodGet, "/api/v5/system/status", nil)
			if ok {
				harness.Equal(harness.NewChecks(t), "the authentication in force is "+tc.inForce, tc.inForce, status.Authentication)
			}
			healthy(t, s, "under "+tc.method+", the probe exits 0 and prints nothing")
		})
	}
}

// generatedPassword opens the line Sonarr logs when it makes the first
// user's password itself, the password following it.
const generatedPassword = "created 'sonarr' with the password:" //nolint:gosec // G101: a log line's opening words, which the password follows; no password

// TestInitialPassword checks the first login, which Sonarr asks for from
// the start: with SONARR__AUTH__INITIALPASSWORD set, the user sonarr
// signs in with it, and no password is logged; with it unset, Sonarr makes
// one and logs it once, which the README tells a user to read.
func TestInitialPassword(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	t.Run("set", func(t *testing.T) {
		t.Parallel()
		s := serveSignedIn(t, e, image)
		checks := harness.NewChecks(t)
		signIn(t, s)
		logs, err := s.C.Logs(t.Context())
		checks.True("Sonarr's log can be read", err == nil, errText(err))
		harness.Equal(checks, "Sonarr logs no password it made", 0, strings.Count(logs, generatedPassword))
	})
	t.Run("unset", func(t *testing.T) {
		t.Parallel()
		s := serveFresh(t, e, image)
		checks := harness.NewChecks(t)
		err := logged(t.Context(), s.C, generatedPassword, harness.ServeWithin)
		checks.True("Sonarr logs the password it made", err == nil, errText(err))
		logs, err := s.C.Logs(t.Context())
		checks.True("Sonarr's log can be read", err == nil, errText(err))
		harness.Equal(checks, "Sonarr logs it exactly once", 1, strings.Count(logs, generatedPassword))
	})
}
