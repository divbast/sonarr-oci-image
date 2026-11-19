// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"slices"
	"testing"
	"time"

	"github.com/divbast/ci/contract"
)

// What the image's contract says of every container here.
const (
	// tmpfsSize is the size of /tmp the README states. Every container
	// gets exactly this, so no check passes on more room than the
	// contract grants.
	tmpfsSize = "16m"
	// appPort serves the page, and /healthz beside it.
	appPort = 8080
)

// binary is the health probe, which the image's HEALTHCHECK and the
// contract's Exec run.
const binary = "/usr/local/bin/divbast-sonarr"

// serverArgv is PID 1's argv: the image's ENTRYPOINT, since nothing runs
// before Sonarr.
var serverArgv = []string{"/app/bin/Sonarr", "-nobrowser", "-exitimmediately", "-data=/config"}

// healthCmd is the shipped health probe, as the image's HEALTHCHECK runs
// it and a Podman user passes it with --health-cmd.
var healthCmd = []string{binary, "health"}

// TestContract runs divbast's confinement contract against the image, on a
// network with no route out: no capability, no new privileges, a seccomp
// filter, a read-only root and no writable mount that can execute, uid
// 65532, the server as PID 1, no shell and no package manager, one
// listener, no egress, the HEALTHCHECK and health on the app port.
func TestContract(t *testing.T) {
	t.Parallel()
	_, image := underTest(t)
	contract.Run(t, &contract.Config{
		Image:      image,
		AppPort:    appPort,
		HealthPort: 0,
		HealthPath: "/healthz",
		TmpfsSize:  tmpfsSize,
		PID1:       serverArgv,
		Exec:       []string{binary, "version"},
		HealthCmd:  healthCmd,
		Healthcheck: contract.Healthcheck{
			Test:        slices.Concat([]string{"CMD"}, healthCmd),
			Interval:    30 * time.Second,
			Timeout:     10 * time.Second,
			StartPeriod: 60 * time.Second,
			Retries:     3,
		},
	})
}
