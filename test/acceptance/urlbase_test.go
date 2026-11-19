// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"testing"

	"github.com/divbast/ci/harness"
)

// urlBase is the URL base a reverse proxy serving Sonarr under a path
// gives it.
const urlBase = "/sonarr"

// urlBaseConfig is a config.xml whose only setting is the URL base, as
// the UI's Settings saves one, or a /config another image left carries.
const urlBaseConfig = "<Config>\n  <UrlBase>" + urlBase + "</UrlBase>\n</Config>\n"

// TestHealthyWithURLBase runs the image with a URL base, as behind a
// reverse proxy that serves it under a path: set in the environment, and
// set in config.xml alone. Sonarr then answers /ping only under the
// base, and redirects the bare path with a 307, which the probe does not
// follow: the probe must ask under the base, wherever it was set, and
// stay healthy.
func TestHealthyWithURLBase(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	for _, tc := range []struct {
		name      string
		env       map[string]string
		configXML string
	}{
		{name: "from the environment", env: map[string]string{"SONARR__SERVER__URLBASE": urlBase}},
		{name: "from config.xml", configXML: urlBaseConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vol := configVolume(t, e)
			if tc.configXML != "" {
				prepare(t, e, image, harness.Contract(tmpfsSize), vol, "write", "config.xml", tc.configXML)
			}
			s := serveSonarr(t, e, image, urlBase+pingPath, keyed(tc.env), vol)
			checks := harness.NewChecks(t)

			healthy(t, s, "with the URL base "+urlBase+" "+tc.name+", the probe exits 0 and prints nothing")
			resp, err := s.Get(t.Context(), urlBase+pingPath, nil)
			harness.Answers(t, "GET "+urlBase+pingPath+" from the host answers 200", http.StatusOK, resp, err)
			if err == nil {
				harness.Equal(checks, "GET "+urlBase+pingPath+" from the host is exactly upstream's body", pingBody, string(resp.Body))
			}
			harness.StatusIs(t, s, "GET "+pingPath+" from the host, without the URL base, answers 307",
				http.StatusTemporaryRedirect, pingPath)
		})
	}
}
