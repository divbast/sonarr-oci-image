// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package probe_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/divbast/sonarr-oci-image/internal/probe"
)

const key = "0123456789abcdef"

// sonarr serves what Sonarr does under prefix: /ping anonymously, and
// the system status to the right key only. Each field overrides one answer.
type sonarr struct {
	prefix     string
	pingStatus int
	appName    string
	version    string
}

func (p sonarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case p.prefix + "/ping":
		w.WriteHeader(p.pingStatus)
		if p.pingStatus == http.StatusOK {
			_, _ = io.WriteString(w, `{"status":"OK"}`)
		} else {
			_, _ = io.WriteString(w, `{"status":"Error"}`)
		}
	case p.prefix + "/api/v5/system/status":
		if r.Header.Get("X-Api-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"appName":"`+p.appName+`","version":"`+p.version+`","isDocker":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func healthy() sonarr {
	return sonarr{pingStatus: http.StatusOK, appName: "Sonarr", version: "5.0.0.2997"}
}

func run(t *testing.T, h http.Handler, o probe.Options) (code int, stderr string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if o.Base == "" {
		o.Base = srv.URL
	}
	var buf bytes.Buffer
	code = probe.Probe(t.Context(), o, &buf)
	return code, buf.String()
}

func opts() probe.Options {
	return probe.Options{APIKey: key, Version: "5.0.0.2997", Within: 2 * time.Second}
}

func TestProbeHealthy(t *testing.T) {
	t.Parallel()
	code, stderr := run(t, healthy(), opts())
	if code != 0 || stderr != "" {
		t.Fatalf("Probe = %d, %q; want 0 and nothing", code, stderr)
	}
}

func TestProbePingFails(t *testing.T) {
	t.Parallel()
	h := healthy()
	h.pingStatus = http.StatusInternalServerError
	code, stderr := run(t, h, opts())
	if code != 1 || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "ping: ") {
		t.Fatalf("Probe = %d, %q; want 1 and one ping: line", code, stderr)
	}
}

func TestProbePingWrongBody(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = io.WriteString(w, `{"status":"Error"}`)
			return
		}
		healthy().ServeHTTP(w, r)
	})
	code, stderr := run(t, h, opts())
	if code != 1 || !strings.HasPrefix(stderr, "ping: ") {
		t.Fatalf("Probe = %d, %q; want 1 and a ping: line", code, stderr)
	}
}

func TestProbeStatusWrongApp(t *testing.T) {
	t.Parallel()
	h := healthy()
	h.appName = "Prowlarr"
	code, stderr := run(t, h, opts())
	if code != 1 || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "status: ") || !strings.Contains(stderr, "Prowlarr") {
		t.Fatalf("Probe = %d, %q; want 1 and one status: line naming Prowlarr", code, stderr)
	}
}

func TestProbeStatusWrongVersion(t *testing.T) {
	t.Parallel()
	h := healthy()
	h.version = "5.0.0.2941"
	code, stderr := run(t, h, opts())
	if code != 1 || !strings.HasPrefix(stderr, "status: ") ||
		!strings.Contains(stderr, "5.0.0.2941") || !strings.Contains(stderr, "5.0.0.2997") {
		t.Fatalf("Probe = %d, %q; want 1 and a status: line naming both versions", code, stderr)
	}
}

func TestProbeNeedsKey(t *testing.T) {
	t.Parallel()
	o := opts()
	o.APIKey = "wrong"
	code, stderr := run(t, healthy(), o)
	if code != 1 || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "status: ") || !strings.Contains(stderr, "401") {
		t.Fatalf("Probe = %d, %q; want 1 and one status: line naming 401", code, stderr)
	}
}

func TestProbeURLBase(t *testing.T) {
	t.Parallel()
	h := healthy()
	h.prefix = "/sonarr"
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base, err := probe.Base(func(k string) string {
		switch k {
		case "SONARR__SERVER__URLBASE":
			return "sonarr/"
		case "SONARR__SERVER__PORT":
			return strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
		}
		return ""
	}, missing)
	if want := srv.URL + "/sonarr"; err != nil || base != want {
		t.Fatalf("Base = %q, %v; want %q", base, err, want)
	}
	o := opts()
	o.Base = base
	var stderr bytes.Buffer
	if code := probe.Probe(t.Context(), o, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("Probe = %d, %q; want 0 and nothing", code, stderr.String())
	}
}

func TestBaseDefaults(t *testing.T) {
	t.Parallel()
	got, err := probe.Base(func(string) string { return "" }, missing)
	if want := "http://127.0.0.1:8080"; err != nil || got != want {
		t.Fatalf("Base = %q, %v; want %q", got, err, want)
	}
}

// TestBaseURLBase checks where the URL base comes from, as Sonarr takes
// it: SONARR__SERVER__URLBASE when it is set, else config.xml's
// UrlBase, which the UI's Settings writes; and that either is given a
// leading / and no trailing /, and means nothing once those are trimmed.
func TestBaseURLBase(t *testing.T) {
	t.Parallel()
	const root = "http://127.0.0.1:8080"
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "SONARR__SERVER__URLBASE" {
				return v
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name   string
		getenv func(string) string
		file   func() ([]byte, error)
		want   string
	}{
		{name: "env wins over config.xml", getenv: env("/sonarr"), file: urlBaseFile("/other"), want: root + "/sonarr"},
		{name: "env needs no file", getenv: env("/sonarr"), file: missing, want: root + "/sonarr"},
		{name: "config.xml alone", getenv: env(""), file: urlBaseFile("/sonarr"), want: root + "/sonarr"},
		{name: "config.xml without a leading slash, with a trailing one", getenv: env(""), file: urlBaseFile("sonarr/"), want: root + "/sonarr"},
		{name: "config.xml with white space, as Sonarr trims it", getenv: env(""), file: urlBaseFile(" /sonarr/ "), want: root + "/sonarr"},
		{name: "config.xml's / is none", getenv: env(""), file: urlBaseFile("/"), want: root},
		{name: "config.xml's empty UrlBase is none", getenv: env(""), file: urlBaseFile(""), want: root},
		{name: "config.xml with no UrlBase is none", getenv: env(""), file: file(`<Config><ApiKey>k</ApiKey></Config>`), want: root},
		{name: "neither is none", getenv: env(""), file: missing, want: root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := probe.Base(tc.getenv, tc.file); err != nil || got != tc.want {
				t.Fatalf("Base = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestBaseReportsInvalidConfig has Base find config.xml invalid while
// SONARR__SERVER__URLBASE is unset: as ReadConfig gives one still invalid
// after its re-reads, and as bytes that do not decode. Base must say so,
// not guess there is no URL base, since that guess sends the probe where
// Sonarr only redirects. With SONARR__SERVER__URLBASE set, config.xml is
// not needed, and so is no error.
func TestBaseReportsInvalidConfig(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name string
			file func() ([]byte, error)
		}{
			{name: "as ReadConfig gives up on it", file: func() ([]byte, error) {
				return probe.ReadConfig(t.Context(), newRewrite(partial).read)
			}},
			{name: "as bytes that do not decode", file: file(partial)},
		} {
			base, err := probe.Base(func(string) string { return "" }, tc.file)
			if base != "" || err == nil ||
				!strings.Contains(err.Error(), "SONARR__SERVER__URLBASE") || !strings.Contains(err.Error(), "config.xml is invalid:") {
				t.Errorf("%s: Base = %q, %v; want no base and an error naming SONARR__SERVER__URLBASE and an invalid config.xml", tc.name, base, err)
			}
		}
		base, err := probe.Base(func(k string) string {
			if k == "SONARR__SERVER__URLBASE" {
				return "/sonarr"
			}
			return ""
		}, file(partial))
		if want := "http://127.0.0.1:8080/sonarr"; err != nil || base != want {
			t.Errorf("with SONARR__SERVER__URLBASE set: Base = %q, %v; want %q", base, err, want)
		}
	})
}

// partial is config.xml as a read can find it while Sonarr writes it:
// cut off part way through.
const partial = "<Config>\n  <UrlBase>/sonarr</Url"

// rewrite is config.xml as Sonarr rewrites it in place, read again and
// again: each read gives the next of contents, and the last once they run
// out. at holds when each read came, on synctest's clock, from when the
// rewrite was made.
type rewrite struct {
	start    time.Time
	contents []string
	at       []time.Duration
}

func newRewrite(contents ...string) *rewrite {
	return &rewrite{start: time.Now(), contents: contents}
}

func (r *rewrite) read() ([]byte, error) {
	r.at = append(r.at, time.Since(r.start))
	return []byte(r.contents[min(len(r.at), len(r.contents))-1]), nil
}

// TestReadConfigRetriesTruncated has the first read find config.xml as
// Sonarr leaves it between truncating it and writing it again: empty.
// ReadConfig must read it again 40 ms later, and return what that read
// gives.
func TestReadConfigRetriesTruncated(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		valid := urlBaseXML("/sonarr")
		r := newRewrite("", valid)
		got, err := probe.ReadConfig(t.Context(), r.read)
		if err != nil || string(got) != valid {
			t.Fatalf("ReadConfig = %q, %v; want %q", got, err, valid)
		}
		if want := []time.Duration{0, 40 * time.Millisecond}; !slices.Equal(r.at, want) {
			t.Fatalf("reads came at %v, want %v", r.at, want)
		}
	})
}

// TestReadConfigMissing reads a /config that holds no config.xml, through
// os.Root as the probe does. That is nothing and no error, at once: a
// setup that sets everything in the environment needs no config.xml.
func TestReadConfigMissing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		start, reads := time.Now(), 0
		got, err := probe.ReadConfig(t.Context(), func() ([]byte, error) {
			reads++
			return root.ReadFile("config.xml")
		})
		if got != nil || err != nil {
			t.Fatalf("ReadConfig = %q, %v; want nil and no error", got, err)
		}
		if took := time.Since(start); reads != 1 || took != 0 {
			t.Fatalf("ReadConfig read %d times over %v; want once, with no wait", reads, took)
		}
	})
}

// TestReadConfigUnreadable has the first read fail for a reason other
// than config.xml's absence: permission denied, as os.Root reports it. A
// read after it would find a valid config.xml, but a refused read is no
// file caught mid-write: ReadConfig must not read it again, and must
// return an error that starts "config.xml is unreadable:", which Base must
// then report rather than guess there is no URL base.
func TestReadConfigUnreadable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start, reads := time.Now(), 0
		read := func() ([]byte, error) {
			reads++
			if reads == 1 {
				return nil, &fs.PathError{Op: "openat", Path: "config.xml", Err: fs.ErrPermission}
			}
			return []byte(urlBaseXML("/sonarr")), nil
		}
		got, err := probe.ReadConfig(t.Context(), read)
		if got != nil || !errors.Is(err, fs.ErrPermission) || !strings.HasPrefix(err.Error(), "config.xml is unreadable:") {
			t.Fatalf("ReadConfig = %q, %v; want nil and an error starting %q that is fs.ErrPermission", got, err, "config.xml is unreadable:")
		}
		if took := time.Since(start); reads != 1 || took != 0 {
			t.Fatalf("ReadConfig read %d times over %v; want once, with no wait", reads, took)
		}
		reads = 0
		base, err := probe.Base(func(string) string { return "" }, func() ([]byte, error) {
			return probe.ReadConfig(t.Context(), read)
		})
		if base != "" || err == nil ||
			!strings.Contains(err.Error(), "SONARR__SERVER__URLBASE") || !strings.Contains(err.Error(), "config.xml is unreadable:") {
			t.Fatalf("Base = %q, %v; want no base and an error naming SONARR__SERVER__URLBASE and an unreadable config.xml", base, err)
		}
	})
}

// TestReadConfigPersistentlyInvalid has every read find config.xml cut
// off part way through. ReadConfig must read it 5 times, 40 ms apart,
// then stop, with no wait after the last read, and an error that starts
// "config.xml is invalid:".
func TestReadConfigPersistentlyInvalid(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		r := newRewrite(partial)
		got, err := probe.ReadConfig(t.Context(), r.read)
		if got != nil || err == nil || !strings.HasPrefix(err.Error(), "config.xml is invalid:") {
			t.Fatalf("ReadConfig = %q, %v; want nil and an error starting %q", got, err, "config.xml is invalid:")
		}
		want := []time.Duration{0, 40 * time.Millisecond, 80 * time.Millisecond, 120 * time.Millisecond, 160 * time.Millisecond}
		if !slices.Equal(r.at, want) {
			t.Fatalf("reads came at %v, want %v", r.at, want)
		}
		if took := time.Since(r.start); took != want[len(want)-1] {
			t.Fatalf("ReadConfig took %v, want %v: no wait after the last read", took, want[len(want)-1])
		}
	})
}

// TestReadConfigCancelled ends ReadConfig's context 60 ms in, while it
// waits to read a config.xml that is cut off for the third time. It must
// stop waiting then, with an error that says config.xml is invalid and
// that the wait was cut short.
func TestReadConfigCancelled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(60*time.Millisecond, cancel)
		r := newRewrite(partial)
		got, err := probe.ReadConfig(ctx, r.read)
		if got != nil || !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "config.xml is invalid:") {
			t.Fatalf("ReadConfig = %q, %v; want nil and an error starting %q that is context.Canceled", got, err, "config.xml is invalid:")
		}
		if took := time.Since(r.start); took != 60*time.Millisecond || len(r.at) != 2 {
			t.Fatalf("ReadConfig read %d times over %v; want twice over 60ms", len(r.at), took)
		}
	})
}

// file gives a config.xml holding xml.
func file(xml string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(xml), nil }
}

// urlBaseXML is a config.xml whose UrlBase is v.
func urlBaseXML(v string) string {
	return "<Config><Port>8080</Port><UrlBase>" + v + "</UrlBase></Config>"
}

// urlBaseFile gives a config.xml whose UrlBase is v.
func urlBaseFile(v string) func() ([]byte, error) { return file(urlBaseXML(v)) }

// missing is no config.xml, as ReadConfig gives it: nothing, and no
// error.
func missing() ([]byte, error) { return nil, nil }

func TestProbeWithin(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	o := opts()
	o.Within = 50 * time.Millisecond
	start := time.Now()
	code, stderr := run(t, h, o)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Probe took %v, want under 1s", took)
	}
	if code != 1 || !strings.Contains(stderr, "ping: ") || !strings.Contains(stderr, "status: ") {
		t.Fatalf("Probe = %d, %q; want 1 with both lines", code, stderr)
	}
}

func TestAPIKey(t *testing.T) {
	t.Parallel()
	keyFile := file(`<Config><ApiKey>fromfile</ApiKey><Port>8080</Port></Config>`)
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "SONARR__AUTH__APIKEY" {
				return v
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name    string
		getenv  func(string) string
		file    func() ([]byte, error)
		want    string
		wantErr []string
	}{
		{name: "env wins", getenv: env("fromenv"), file: keyFile, want: "fromenv"},
		{name: "env needs no file", getenv: env("fromenv"), file: missing, want: "fromenv"},
		{name: "file alone", getenv: env(""), file: keyFile, want: "fromfile"},
		{name: "file with white space around the key, as Sonarr trims it", getenv: env(""), file: file("<Config><ApiKey>\n  fromfile \t</ApiKey></Config>"), want: "fromfile"},
		{name: "file with white space alone is no key", getenv: env(""), file: file("<Config><ApiKey> \n </ApiKey></Config>"), wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
		{name: "neither", getenv: env(""), file: missing, wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
		{name: "an invalid config.xml", getenv: env(""), file: file(partial), wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml is invalid"}},
		{name: "no key in file", getenv: env(""), file: file(`<Config></Config>`), wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := probe.APIKey(tc.getenv, tc.file)
			if len(tc.wantErr) == 0 {
				if err != nil || got != tc.want {
					t.Fatalf("APIKey = %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("APIKey = %q, nil; want an error", got)
			}
			for _, s := range tc.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
		})
	}
}

// TestProbeRefusesRedirect has Sonarr serve under /sonarr only, and
// redirect every other path there with a 307, as UrlBaseMiddleware does.
// A probe whose Base lacks the URL base must say so, not follow it.
func TestProbeRefusesRedirect(t *testing.T) {
	t.Parallel()
	h := healthy()
	h.prefix = "/sonarr"
	inner := http.NewServeMux()
	inner.Handle("/sonarr/", h)
	inner.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/sonarr"+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	code, stderr := run(t, inner, opts())
	if code != 1 || strings.Count(stderr, "\n") != 2 ||
		!strings.Contains(stderr, "ping: ") || !strings.Contains(stderr, "status: ") ||
		strings.Count(stderr, "307") != 2 {
		t.Fatalf("Probe = %d, %q; want 1 with a ping: and a status: line naming 307", code, stderr)
	}
}

// TestProbePingStarting has /ping answer as Sonarr does before it has
// finished starting: 503, with a plain-text body. The probe must say so in
// one ping: line, not pass, and not crash on a body that is not JSON.
func TestProbePingStarting(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "Sonarr is starting up, please try again later")
			return
		}
		healthy().ServeHTTP(w, r)
	})
	code, stderr := run(t, h, opts())
	if code != 1 || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "ping: ") || !strings.Contains(stderr, "503") {
		t.Fatalf("Probe = %d, %q; want 1 and one ping: line naming 503", code, stderr)
	}
}
