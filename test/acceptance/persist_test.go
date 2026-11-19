// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/divbast/ci/harness"
)

// TestStatePersists checks that what the API creates lives on the /config
// volume: a tag created through the API is still there, under its ID and
// label, after the engine restarts the container. The root is read-only
// and a restart empties /tmp, so the volume is the one place it can have
// been kept.
func TestStatePersists(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	s := serveFresh(t, e, image)

	made, ok := callAPI[tag](t, s, "POST /api/v5/tag answers 201", http.StatusCreated,
		http.MethodPost, "/api/v5/tag", tag{Label: "persisted"})
	if !ok {
		return
	}
	if err := s.Restart(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	kept, ok := callAPI[tag](t, s, "after a restart, GET /api/v5/tag/<its id> answers 200", http.StatusOK,
		http.MethodGet, "/api/v5/tag/"+strconv.Itoa(made.ID), nil)
	if ok {
		harness.Equal(harness.NewChecks(t), "after a restart, the tag keeps its label", "persisted", kept.Label)
	}
}
