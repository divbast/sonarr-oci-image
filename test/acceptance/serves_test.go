// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"mime"
	"net/http"
	"net/url"
	"testing"

	"github.com/divbast/ci/harness"
)

// pingBody is exactly what /ping answers once Sonarr is up: upstream's
// JSON, compact, as it sends it.
const pingBody = `{"status":"OK"}`

// loginTitle is the title of the UI's login page, which only the UI the
// frontend stage built carries, and which Sonarr serves to anyone.
const loginTitle = "<title>Login - Sonarr</title>"

// TestServes checks the app port from a client outside the container,
// through the published port, with no session and no API key, as a fresh
// install meets a visitor: /ping answers 200 with exactly upstream's body;
// / sends a visitor with no session to the login page, since Sonarr asks
// for a login from the start; the login page answers 200 with the UI's
// HTML; and the API refuses a client without the key with 401.
func TestServes(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	resp, err := s.Get(ctx, pingPath, nil)
	harness.Answers(t, "GET /ping from the host answers 200", http.StatusOK, resp, err)
	if err == nil {
		harness.Equal(checks, "GET /ping from the host is exactly upstream's body", pingBody, string(resp.Body))
	}

	resp, err = s.Get(ctx, "/", nil)
	harness.Answers(t, "GET / from the host with no session answers 302", http.StatusFound, resp, err)
	if err == nil {
		where, perr := url.Parse(resp.Header.Get("Location"))
		got := ""
		if perr == nil {
			got = where.Path
		}
		harness.Equal(checks, "GET / from the host with no session goes to the login page", "/login", got)
	}

	resp, err = s.Get(ctx, "/login", nil)
	harness.Answers(t, "GET /login from the host answers 200", http.StatusOK, resp, err)
	if err == nil {
		media, _, merr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if merr != nil {
			media = merr.Error()
		}
		harness.Equal(checks, "GET /login from the host is HTML", "text/html", media)
		harness.Contains(t, "GET /login from the host is the UI's login page", loginTitle, string(resp.Body))
	}

	harness.StatusIs(t, s, "GET /api/v5/system/status from the host without the API key answers 401",
		http.StatusUnauthorized, "/api/v5/system/status")
}
