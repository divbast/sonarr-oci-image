// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// restarting is what Sonarr logs once its host has stopped for a
// restart and it builds the host again, in the same process.
const restarting = "Application restart requested, reinitializing host"

// restartWithin bounds a restart from the UI, as harness.ServeWithin
// bounds a start: Sonarr's host starts in under 3 s, and a restart stops
// it first.
const restartWithin = time.Minute

// TestRestartsInProcess restarts Sonarr as its UI does, with POST
// /api/v5/system/restart. Sonarr stops its host and builds it again
// inside the one process, which, as PID 1, must not exit: the container
// stops with it, and no engine starts it again unless told to. So the
// container must still be running, its PID 1 the process the engine
// started, with the same argv, /ping must answer 200 again, after
// Sonarr has said it is restarting, within a minute, and the probe must
// find it healthy.
func TestRestartsInProcess(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	before, err := s.C.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	harness.RunRecords(t, s.Probe, "assert-pid1", serverArgv...)

	resp, err := api(ctx, s, http.MethodPost, "/api/v5/system/restart", nil)
	harness.Answers(t, "POST /api/v5/system/restart answers 200", http.StatusOK, resp, err)
	err = servesAgain(ctx, s)
	checks.True("Sonarr says it is restarting, and /ping answers 200 again within "+restartWithin.String(), err == nil, errText(err))
	healthy(t, s, "after the restart, the probe exits 0 and prints nothing")

	after, err := s.C.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checks.True("the container is still running", after.State.Running, after.State.Status)
	checks.True("PID 1 is still the process the engine started", after.State.StartedAt.Equal(before.State.StartedAt),
		fmt.Sprintf("started at %s, and at %s before the restart", after.State.StartedAt, before.State.StartedAt))
	harness.RunRecords(t, s.Probe, "assert-pid1", serverArgv...)
}

// servesAgain waits until s's log says Sonarr is restarting, and then
// asks for /ping every 250 ms until it answers 200, which only the host
// built after that can. The two waits share restartWithin. It returns an
// error at once if the container stops, and once restartWithin has
// passed.
func servesAgain(ctx context.Context, s *harness.Serving) error {
	const every = 250 * time.Millisecond
	deadline := time.Now().Add(restartWithin)
	if err := logged(ctx, s.C, restarting, restartWithin); err != nil {
		return err
	}
	// What each answer but 200, the container still running, is wrapped
	// in: only these does Poll ask again after, and when it stops on one,
	// the error needs the container's log
	notServing := fmt.Errorf("GET %s does not answer 200", pingPath)
	// What is left of restartWithin, though never less than one pause, so
	// that /ping is asked at least once however long the log took
	within := max(time.Until(deadline).Round(time.Millisecond), every)
	err := harness.Poll(ctx, within, every, func(tryCtx context.Context) error {
		resp, err := s.Get(tryCtx, pingPath, nil)
		switch {
		case err == nil && resp.Status == http.StatusOK:
			return nil
		case err == nil:
			err = fmt.Errorf("status %d", resp.Status)
		}
		// The engine is asked with ctx, as in logged
		ins, ierr := s.C.Inspect(ctx)
		switch {
		case ierr != nil:
			return ierr
		case !ins.State.Running:
			return harness.WithLog(ctx, s.C, fmt.Errorf("the container stopped, exit status %d", ins.State.ExitCode))
		}
		return harness.NotYet(fmt.Errorf("%w: %w", notServing, err))
	})
	if errors.Is(err, notServing) {
		return harness.WithLog(ctx, s.C, err)
	}
	return err
}
