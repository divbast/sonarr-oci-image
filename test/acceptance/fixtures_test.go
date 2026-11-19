// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// Where Sonarr keeps its state, and what it reads from its environment.
const (
	// configDir is Sonarr's data directory, the ENTRYPOINT's -data: the
	// one place it writes but /tmp.
	configDir = "/config"
	// keyEnv names the variable that sets the API key, over config.xml's.
	keyEnv = "SONARR__AUTH__APIKEY"
	// pingPath is Sonarr's health route, which answers anyone.
	pingPath = "/ping"
)

// suiteProbe is the suite's own probe, which the harness builds for the
// image's architecture and mounts at [harness.AppProbePath].
const suiteProbe = "./test/probe"

// asRoot runs a container as root, with the engine's default
// capabilities, CAP_CHOWN among them, on a read-only root and with no new
// privileges: how the suite leaves a volume as root on the host, or an
// image run as root, would leave it.
var asRoot = harness.Confinement{User: "0:0", ReadOnly: true, NoNewPrivileges: true}

// configVolume is a fresh named volume at /config, as a user mounts one.
// Each engine gives it the owner and mode of the image's empty /config,
// uid 65532's, on its first mount.
func configVolume(tb testing.TB, e harness.Engine) harness.Mount {
	tb.Helper()
	name, err := e.Volume(tb.Context(), tb, "")
	if err != nil {
		tb.Fatal(err)
	}
	return harness.Mount{Source: name, Target: configDir, Volume: true}
}

// keyed returns env with the API key set, so a test can call the API.
func keyed(env map[string]string) map[string]string {
	keyed := maps.Clone(env)
	if keyed == nil {
		keyed = map[string]string{}
	}
	keyed[keyEnv] = testAPIKey
	return keyed
}

// upstreamHosts are where Sonarr goes by itself: services.sonarr.tv for
// its notices, the time, update checks and scene mappings, Skyhook for
// series metadata, TheXEM for scene numbering, and Servarr's Sentry for
// error reports. Every container served here finds each at 127.0.0.1,
// which refuses the connection at once: no test needs them, none should
// wait on them, and a suite should not call them a dozen times over.
var upstreamHosts = []string{"services.sonarr.tv", "skyhook.sonarr.tv", "thexem.info", "sentry.sonarr.tv"}

// analyticsOff turns off what Sonarr sends upstream, its error reports
// among them. The image ships with analytics on, as upstream does: the
// contract's own container runs that way.
const analyticsOff = "SONARR__LOG__ANALYTICSENABLED"

// noUpstream is the --add-host flags that send each of upstreamHosts to
// loopback.
func noUpstream() []string {
	extra := make([]string, 0, 2*len(upstreamHosts))
	for _, h := range upstreamHosts {
		extra = append(extra, "--add-host", h+":127.0.0.1")
	}
	return extra
}

// engineLogs is the flags that have engine e keep every line Sonarr logs,
// so a test that waits for one, or reads why a container stopped, finds
// it. Podman logs to journald by default where systemd runs, and journald
// drops a unit's messages past its rate limit: Sonarr's first start logs
// each of its database migrations, thousands of lines, and a suite run
// starting twenty containers at once lost tens of thousands of them, the
// lines its tests wait for among them. Podman's file log, k8s-file, keeps
// them all, as Docker's default, json-file, does already. Where the
// engine keeps its logs is no part of the confinement the contract claims.
func engineLogs(e harness.Engine) []string {
	if e.Name() == "podman" {
		return []string{"--log-driver", "k8s-file"}
	}
	return nil
}

// serveSonarr serves the image under the contract, with env and mounts,
// and waits until readyPath answers. Sonarr listens only once its
// database is migrated, so an answer at its ping route means it is up. The
// container reaches none of Sonarr's own hosts, and sends nothing
// upstream.
func serveSonarr(t *testing.T, e harness.Engine, image, readyPath string, env map[string]string, mounts ...harness.Mount) *harness.Serving {
	t.Helper()
	env = maps.Clone(env)
	if env == nil {
		env = map[string]string{}
	}
	env[analyticsOff] = "False"
	s, err := e.Serve(t.Context(), t, &harness.ServeSpec{
		Run: harness.RunSpec{
			Image:     image,
			Confine:   harness.Contract(tmpfsSize),
			Env:       env,
			Mounts:    mounts,
			HealthCmd: healthCmd,
			Extra:     append(noUpstream(), engineLogs(e)...),
		},
		AppPort:   appPort,
		ReadyPath: readyPath,
	})
	if err != nil {
		t.Fatalf("serving %s: %v", image, err)
	}
	return s
}

// serveFresh serves the image with the API key, on a fresh /config, as a
// new install runs.
func serveFresh(t *testing.T, e harness.Engine, image string) *harness.Serving {
	t.Helper()
	return serveSonarr(t, e, image, pingPath, keyed(nil), configVolume(t, e))
}

// testPassword is the first password the suite gives Sonarr's login,
// through SONARR__AUTH__INITIALPASSWORD.
const testPassword = "divbast-acceptance-only-0123" //nolint:gosec // G101: a made-up password, given only to the throwaway containers this suite runs

// serveSignedIn serves the image as serveFresh does, with testPassword as
// the first user's password, so a test can sign in to the UI with
// [signIn]: what Sonarr serves outside its API, a backup's download among
// it, takes the UI's session and not the API key.
func serveSignedIn(t *testing.T, e harness.Engine, image string) *harness.Serving {
	t.Helper()
	s := serveSonarr(t, e, image, pingPath, keyed(map[string]string{"SONARR__AUTH__INITIALPASSWORD": testPassword}), configVolume(t, e))
	// Sonarr makes the first user once its host has started, just after it
	// first answers /ping, so a sign-in sent at once can come before there
	// is anyone to sign in as: wait for it to say the user exists
	if err := logged(t.Context(), s.C, userCreated, harness.ServeWithin); err != nil {
		t.Fatalf("Sonarr never made its first user: %v", err)
	}
	return s
}

// userCreated is what Sonarr logs once it has made the first user with
// the password SONARR__AUTH__INITIALPASSWORD gives.
const userCreated = "created 'sonarr' with the configured password"

// signIn signs in to s's UI as the user sonarr with testPassword, as the
// login page posts its form, and returns the session cookie Sonarr sets,
// as a Cookie header's value. It returns false, having failed a check,
// when the login fails or sets no session.
func signIn(t *testing.T, s *harness.Serving) (string, bool) {
	t.Helper()
	checks := harness.NewChecks(t)
	form := url.Values{"username": {"sonarr"}, "password": {testPassword}}.Encode()
	resp, err := s.Do(t.Context(), nil, http.MethodPost, "/login",
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, []byte(form))
	harness.Answers(t, "POST /login as sonarr with the password given answers 302", http.StatusFound, resp, err)
	if err != nil || resp.Status != http.StatusFound {
		return "", false
	}
	where := resp.Header.Get("Location")
	failed := strings.Contains(where, "loginFailed")
	checks.True("the login succeeds, sending the user on rather than back to the login page", !failed, "Location: "+where)
	if failed {
		return "", false
	}
	for _, c := range resp.Header.Values("Set-Cookie") {
		if name, _, _ := strings.Cut(c, "="); name == "SonarrAuth" {
			cookie, _, _ := strings.Cut(c, ";")
			checks.True("the login sets Sonarr's session cookie", true, "SonarrAuth")
			return cookie, true
		}
	}
	checks.True("the login sets Sonarr's session cookie", false, "Set-Cookie: "+strings.Join(resp.Header.Values("Set-Cookie"), "; "))
	return "", false
}

// api sends method path to s's API from the host with the API key, and
// with body as JSON when it is not nil.
func api(ctx context.Context, s *harness.Serving, method, path string, body any) (harness.Response, error) {
	header := http.Header{"X-Api-Key": {testAPIKey}}
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return harness.Response{}, err
		}
		header.Set("Content-Type", "application/json")
	}
	return s.Do(ctx, nil, method, path, header, b)
}

// callAPI sends method path to s's API as [api] does, checks that it
// answers want, as the check what, and decodes the answer's JSON into a
// T. It returns false, having failed a check, when either does not hold.
func callAPI[T any](t *testing.T, s *harness.Serving, what string, want int, method, path string, body any) (T, bool) {
	t.Helper()
	var v T
	resp, err := api(t.Context(), s, method, path, body)
	harness.Answers(t, what, want, resp, err)
	if err != nil || resp.Status != want {
		return v, false
	}
	if err := jsonInto(resp, &v); err != nil {
		harness.NewChecks(t).True(what+", in JSON", false, err.Error())
		return v, false
	}
	return v, true
}

// jsonInto decodes resp's body, JSON, into v, and says what the body was
// when it cannot.
func jsonInto(resp harness.Response, v any) error {
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return fmt.Errorf("%w: %s", err, harness.Clip(string(resp.Body)))
	}
	return nil
}

// healthy checks, as the check what, that the shipped probe, run in s's
// container as the engine runs it, exits 0 and prints nothing.
func healthy(t *testing.T, s *harness.Serving, what string) {
	t.Helper()
	checks := harness.NewChecks(t)
	res, err := s.C.Exec(t.Context(), nil, healthCmd...)
	if err != nil {
		checks.True(what, false, err.Error())
		return
	}
	out := string(res.Stdout) + string(res.Stderr)
	checks.True(what, res.Code == 0 && out == "", fmt.Sprintf("exit status %d, printing %s", res.Code, harness.Clip(out)))
}

// prepare runs the suite's own probe with args as the entrypoint of a
// container of image, under confine, with no network and vol mounted, and
// fails t unless it exits 0: how a test puts a volume in the state it
// needs before Sonarr mounts it.
func prepare(t *testing.T, e harness.Engine, image string, confine harness.Confinement, vol harness.Mount, args ...string) {
	t.Helper()
	ctx := t.Context()
	c, err := e.Start(ctx, t, &harness.RunSpec{
		Image:      image,
		Network:    "none",
		Confine:    confine,
		Mounts:     []harness.Mount{vol},
		Entrypoint: []string{harness.AppProbePath},
		Args:       args,
		Probe:      &harness.ProbeSpec{AppPackage: suiteProbe},
	})
	if err != nil {
		t.Fatalf("preparing %s with %q: %v", vol.Target, args, err)
	}
	code, err := c.WaitExit(ctx, harness.ServeWithin)
	if err == nil && code != 0 {
		err = harness.WithLog(ctx, c, fmt.Errorf("exit status %d", code))
	}
	if err != nil {
		t.Fatalf("preparing %s with %q: %v", vol.Target, args, err)
	}
}

// tag is a tag as Sonarr's API gives it.
type tag struct {
	ID    int    `json:"id,omitempty"`
	Label string `json:"label"`
}

// updateSettings is the part of GET /api/v5/settings/update the tests
// read: what config.xml says now.
type updateSettings struct {
	UpdateMechanism string `json:"updateMechanism"`
	Branch          string `json:"branch"`
}

// systemStatus is the part of GET /api/v5/system/status the tests read.
type systemStatus struct {
	PackageUpdateMechanism string `json:"packageUpdateMechanism"`
	Authentication         string `json:"authentication"`
	Branch                 string `json:"branch"`
}

// logged reads c's log every 250 ms until it holds needle, and returns an
// error, with the log, once within has passed, or at once if c stops or
// the engine fails to answer.
func logged(ctx context.Context, c *harness.Container, needle string, within time.Duration) error { //nolint:unparam // each caller's own budget, which harness.ServeWithin and restartWithin each happen to set at a minute today
	// What each look short of needle, c still running, returns: only
	// these does Poll look again after, and when it stops on one, the
	// error needs c's log
	waiting := fmt.Errorf("its log does not say %q", needle)
	// The engine is asked with ctx rather than the try's own, which the
	// budget ends: a call it cut short would come back as the engine's
	// error, returned at once, in place of the budget's
	err := harness.Poll(ctx, within, 250*time.Millisecond, func(context.Context) error {
		logs, err := c.Logs(ctx)
		if err != nil {
			return err
		}
		if strings.Contains(logs, needle) {
			return nil
		}
		ins, err := c.Inspect(ctx)
		switch {
		case err != nil:
			return err
		case !ins.State.Running:
			return harness.WithLog(ctx, c, fmt.Errorf("the container stopped, exit status %d, before its log said %q", ins.State.ExitCode, needle))
		}
		return harness.NotYet(waiting)
	})
	if errors.Is(err, waiting) {
		return harness.WithLog(ctx, c, err)
	}
	return err
}

// errText is err's text, or "ok" for none, for a check's detail.
func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}
