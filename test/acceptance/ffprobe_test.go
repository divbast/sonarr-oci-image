// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// ffprobe is the binary Sonarr runs to read a media file's streams, from
// the folder it runs from. It comes from NuGet, static, without its
// execute bit, which the backend stage gives back.
const ffprobe = "/app/bin/ffprobe"

// TestFFprobeRuns runs ffprobe in a serving container, as Sonarr's own
// user under the contract's confinement, as Sonarr runs it on a library
// scan or an import: it must start, which takes its execute bit and a
// binary that needs nothing the base lacks, and say what it is. With no
// series added, Sonarr never runs it on its own, so nothing else would
// show it broken.
func TestFFprobeRuns(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	res, err := s.C.Exec(t.Context(), nil, ffprobe, "-version")
	if err != nil {
		checks.True(ffprobe+" -version runs", false, err.Error())
		return
	}
	out := string(res.Stdout)
	checks.True(ffprobe+" -version exits 0", res.Code == 0,
		fmt.Sprintf("exit status %d, printing %s", res.Code, harness.Clip(out+string(res.Stderr))))
	checks.True(ffprobe+" -version says it is ffprobe", strings.HasPrefix(out, "ffprobe version "), harness.Clip(out))
}
