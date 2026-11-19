// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"testing"

	"github.com/divbast/ci/harness"
)

// dockerMechanism is how Sonarr's status names the update mechanism
// that leaves updates to the image, which the image's package_info asks
// for: the UI then offers no update of its own, which in a read-only
// image could only fail.
const dockerMechanism = "docker"

// developBranch is the branch Sonarr reports while the image follows
// upstream's v5 develop line, which package_info names: upstream's own
// name for v5 builds, until the image moves to stable (CLAUDE.md, 'Moving
// to stable'). The branch only says what Sonarr's own update check asks
// upstream about.
const developBranch = "develop"

// TestUpdaterIsDocker checks that Sonarr reports Docker as its update
// mechanism, and the develop branch.
func TestUpdaterIsDocker(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	s := serveFresh(t, e, image)

	status, ok := callAPI[systemStatus](t, s, "GET /api/v5/system/status answers 200", http.StatusOK,
		http.MethodGet, "/api/v5/system/status", nil)
	if ok {
		checks := harness.NewChecks(t)
		harness.Equal(checks, "Sonarr's update mechanism is docker", dockerMechanism, status.PackageUpdateMechanism)
		harness.Equal(checks, "Sonarr's branch is develop", developBranch, status.Branch)
	}
}
