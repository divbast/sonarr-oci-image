// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// notWritable is what Sonarr says when it may not write in /config:
// before anything else, it checks the data directory is writable, and
// refuses to start on one that is not.
const notWritable = "Sonarr failed to start: AppFolder /config is not writable"

// nonRecoverableFailure is the exit status Sonarr's ConsoleApp gives a
// SonarrStartupException, which -exitimmediately has it exit with at once
// rather than wait an hour for a key no one presses in a container.
const nonRecoverableFailure = 3

// TestRefusesUnwritableConfig runs the image on a /config its user cannot
// write: a volume root owns, mode 0755, as a directory root made and bound
// in, or a volume an image run as root left, would be. The container must
// exit within a minute, with NonRecoverableFailure, rather than serve with
// nowhere to keep its state, or wait to be told to stop, and its log must
// name /config, since the log is all a user sees of why.
func TestRefusesUnwritableConfig(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, asRoot, vol, "root-own")
	harness.Refuses(t, e, refusing(image, vol), nonRecoverableFailure, "on a /config root owns", notWritable)
}

// TestLogWaitEndsWhenStopped holds the suite's own helper, not anything
// the image guarantees: logged, which the tests that wait for a line in
// Sonarr's log share, must return the moment the container stops, as
// it does on a /config root owns, rather than wait out its budget for a
// line that can no longer come. It asserts on the error, not on the time
// taken, which a slow runner would make flaky: had logged waited, its
// error would be Poll's, "still failing after", wrapping the stopped
// container's.
func TestLogWaitEndsWhenStopped(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	vol := configVolume(t, e)
	prepare(t, e, image, asRoot, vol, "root-own")
	c, err := e.Start(ctx, t, refusing(image, vol))
	if err != nil {
		t.Fatal(err)
	}
	err = logged(ctx, c, "a line Sonarr never logs", harness.ServeWithin)
	text := errText(err)
	harness.NewChecks(t).True("logged returns the stopped container's error at once, not its budget's",
		err != nil && strings.Contains(text, "the container stopped") && !strings.Contains(text, "still failing after"),
		harness.Clip(text))
}

// pidUnwritable is what Sonarr says when it cannot write its PID file.
const pidUnwritable = "Sonarr failed to start: Unable to write PID file /config/sonarr.pid"

// TestExitsOnStartupFailure runs the image on a /config it can write, in
// which its PID file, sonarr.pid, is a directory. Sonarr cannot write
// the file, and fails to start with a SonarrStartupException, which
// upstream calls non-recoverable: unless given -exitimmediately, it then
// waits an hour for a key that no one presses in a container. The
// container must exit within a minute with NonRecoverableFailure, the
// status only -exitimmediately gives that wait, and its log must say why.
func TestExitsOnStartupFailure(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, harness.Contract(tmpfsSize), vol, "mkdir", "sonarr.pid")
	harness.Refuses(t, e, refusing(image, vol), nonRecoverableFailure, "with sonarr.pid a directory", pidUnwritable)
}

// refusing is a run of the image under the contract, with vol at /config
// and no network, for a start Sonarr must refuse.
func refusing(image string, vol harness.Mount) *harness.RunSpec {
	return &harness.RunSpec{
		Image:   image,
		Network: "none",
		Confine: harness.Contract(tmpfsSize),
		Mounts:  []harness.Mount{vol},
	}
}

// unmountedWithin is how long Sonarr may take to give up on a /config
// nothing is mounted at. Measured, it exited 107 and 149 s after it
// started, with 10 to 20 s between its log lines until its check of
// /config refused to start, where a /config it may not write is refused
// within 2 s. 149 s and half again rounds up to four minutes.
const unmountedWithin = 4 * time.Minute

// TestRefusesNoConfigMount runs the image with nothing mounted at /config,
// as a user who forgets the volume does: the image's own /config is empty
// and 65532's, on a read-only root. The container must exit with
// NonRecoverableFailure, naming /config, rather than serve with nowhere to
// keep its state or sit there looking alive. It takes longer than a
// refused /config does, so it gets a budget of its own.
func TestRefusesNoConfigMount(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	checks := harness.NewChecks(t)
	c, err := e.Start(ctx, t, &harness.RunSpec{
		Image:   image,
		Network: "none",
		Confine: harness.Contract(tmpfsSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := c.WaitExit(ctx, unmountedWithin)
	if err != nil {
		checks.True("with no /config mounted, the container exits within "+unmountedWithin.String(), false, harness.WithLog(ctx, c, err).Error())
		return
	}
	harness.Equal(checks, "with no /config mounted, the container exits NonRecoverableFailure", nonRecoverableFailure, code)
	harness.LogContains(t, c, "with no /config mounted, the log says why", notWritable)
}

// migratedConfig is a config.xml as a v4 Sonarr image leaves it in
// /config: upstream's port, 8989, no authentication, which v5 no longer
// accepts from config.xml, the built-in updater on v4's branch, main, and
// the API key that install made.
const migratedConfig = `<Config>
  <BindAddress>*</BindAddress>
  <Port>8989</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>True</LaunchBrowser>
  <ApiKey>` + testAPIKey + `</ApiKey>
  <AuthenticationMethod>None</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <Branch>main</Branch>
  <LogLevel>info</LogLevel>
  <UrlBase></UrlBase>
  <InstanceName>Sonarr</InstanceName>
  <UpdateMechanism>BuiltIn</UpdateMechanism>
</Config>
`

// mechanismChanged and branchChanged are what Sonarr logs as it changes
// config.xml's update mechanism and branch to the Docker mechanism and the
// develop branch package_info names, once its host has started.
const (
	mechanismChanged = "Update mechanism BuiltIn not supported in the current configuration, changing to Docker."
	branchChanged    = "External updater uses branch develop instead of the currently selected main, changing to develop."
)

// TestMigratedConfig runs the image on a /config a v4 image left, whose
// config.xml says port 8989, no authentication, and the built-in updater
// on main, with no API key in the environment. It must serve on 8080,
// which the image sets over config.xml; answer the API with config.xml's
// key; ask for a login, since v5 takes no authentication only from the
// environment; once its host has started, have changed config.xml's
// update mechanism to Docker and its branch to develop; and be healthy,
// the probe reading the API key from config.xml.
func TestMigratedConfig(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, harness.Contract(tmpfsSize), vol, "write", "config.xml", migratedConfig)
	s := serveSonarr(t, e, image, pingPath, nil, vol)
	checks := harness.NewChecks(t)

	harness.StatusIs(t, s, "GET /ping on 8080, from the host, answers 200", http.StatusOK, pingPath)
	err := logged(t.Context(), s.C, hostStarted, harness.ServeWithin)
	checks.True("Sonarr's host finishes starting", err == nil, errText(err))
	harness.LogContains(t, s.C, "Sonarr says it changes BuiltIn to Docker", mechanismChanged)
	harness.LogContains(t, s.C, "Sonarr says it changes main to develop", branchChanged)
	update, ok := callAPI[updateSettings](t, s, "GET /api/v5/settings/update with config.xml's key answers 200", http.StatusOK,
		http.MethodGet, "/api/v5/settings/update", nil)
	if ok {
		harness.Equal(checks, "config.xml's update mechanism is docker", dockerMechanism, update.UpdateMechanism)
		harness.Equal(checks, "config.xml's branch is develop", developBranch, update.Branch)
	}
	status, ok := callAPI[systemStatus](t, s, "GET /api/v5/system/status with config.xml's key answers 200", http.StatusOK,
		http.MethodGet, "/api/v5/system/status", nil)
	if ok {
		harness.Equal(checks, "the authentication in force is forms", "forms", status.Authentication)
	}
	healthy(t, s, "the probe, with config.xml's key, exits 0 and prints nothing")
}
