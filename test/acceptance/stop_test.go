// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"os/exec"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// stopWithin is how long a stop may take, with Sonarr's own hosts refused
// and analytics off, as serveSonarr runs every container. Once its host
// had started, Sonarr stopped in 0.19 to 0.22 s on its own and 0.24 to
// 0.35 s within this suite. Prowlarr, on the same .NET host and the same
// shutdown, took 0.60 to 2.81 s with three suites running at once on one
// host, as on a busy CI runner, and its budget, 2.81 s and half again
// rounded up to 4.3 s, holds here too: well inside the 10 s after which an
// engine kills a PID 1 that has not stopped.
const stopWithin = 4300 * time.Millisecond

// hostStarted is what .NET logs once Sonarr's host has finished
// starting. Sonarr answers /ping just before that, while its host runs
// what it runs on starting, its health checks and their calls upstream
// among it, and a stop in that window waits for it to finish. Waiting for
// this line keeps the stop measured a stop alone, whatever starting comes
// to run.
const hostStarted = "Application started."

// TestStopsPromptly stops a serving container as a user does, with
// `<engine> stop`, once Sonarr has finished starting, and checks it
// exits 0 well before the engine's timeout. The kernel drops a signal to
// PID 1 that it does not handle, so a server that never handles SIGTERM
// keeps running until the engine kills it, ten seconds on, with exit 137.
func TestStopsPromptly(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)
	err := logged(ctx, s.C, hostStarted, harness.ServeWithin)
	checks.True("Sonarr's host finishes starting", err == nil, errText(err))
	if err != nil {
		return
	}

	start := time.Now()
	out, err := exec.CommandContext(ctx, e.Name(), "stop", s.C.ID).CombinedOutput() //nolint:gosec // G204: the engine and the container are the harness's own
	took := time.Since(start)
	checks.True(e.Name()+" stop returns", err == nil, string(out))
	checks.True("the container stops within "+stopWithin.String(), took < stopWithin, took.String())

	code, err := s.C.WaitExit(ctx, time.Second)
	if err != nil {
		checks.True("the container exits", false, err.Error())
		return
	}
	harness.Equal(checks, "the container exits 0", 0, code)
}
