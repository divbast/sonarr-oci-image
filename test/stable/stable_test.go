// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package stable holds the image on Sonarr's v5 develop line, and says
// when that has to end: once upstream publishes v5 stable, the image moves
// to it by hand, as CLAUDE.md's "Moving to stable" says.
package stable

import (
	"os"
	"regexp"
	"testing"
)

// none is the watch's value while Sonarr has no stable v5 release: a
// version no release has. It must share no major, minor and patch with a
// v5 develop build, every one of which is 5.0.0.N, because Renovate offers
// a pre-release that shares all three with the current version, stable
// flag or not.
const none = "0.0.0.0"

// watch finds the Containerfile's line Renovate writes Sonarr's first
// stable v5 release to. It lives in the Containerfile, beside the pins,
// because Renovate's recommended preset ignores every path under test/.
var watch = regexp.MustCompile(`(?m)^# SONARR_STABLE_V5=(\S*)$`)

// TestNoV5StableYet fails once Renovate has written a stable v5 release
// into the watch, naming the steps that move the image to it. Renovate's
// entry for the watch shares a group, and so a branch and a pull request,
// with the develop line's pins, and never merges itself: once this fails
// that pull request, every develop release after it lands there too, so
// the image stays on the develop build it has, which the stable release
// carries. It fails, too, on a Containerfile that has lost the watch, or
// has two, since then nothing would see v5 stable arrive.
func TestNoV5StableYet(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	found := watch.FindAllSubmatch(b, -1)
	if len(found) != 1 {
		t.Fatalf("the Containerfile has %d '# SONARR_STABLE_V5=' lines, want 1: Renovate writes Sonarr's first stable v5 release there", len(found))
	}
	if v := string(found[0][1]); v != none {
		t.Fatalf("Sonarr v5 is stable: release %s. The develop line stops here until the image moves to stable: CLAUDE.md, 'Moving to stable'.", v)
	}
}
