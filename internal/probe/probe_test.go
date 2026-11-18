// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package probe_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	base := probe.Base(func(k string) string {
		switch k {
		case "SONARR__SERVER__URLBASE":
			return "sonarr/"
		case "SONARR__SERVER__PORT":
			return strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
		}
		return ""
	}, missing)
	if want := srv.URL + "/sonarr"; base != want {
		t.Fatalf("Base = %q, want %q", base, want)
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
	got := probe.Base(func(string) string { return "" }, missing)
	if want := "http://127.0.0.1:8080"; got != want {
		t.Fatalf("Base = %q, want %q", got, want)
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
		file   func() (io.ReadCloser, error)
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
		{name: "an invalid config.xml is none", getenv: env(""), file: file(`<Config><UrlBase>`), want: root},
		{name: "neither is none", getenv: env(""), file: missing, want: root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probe.Base(tc.getenv, tc.file); got != tc.want {
				t.Fatalf("Base = %q, want %q", got, tc.want)
			}
		})
	}
}

// file opens a config.xml holding xml.
func file(xml string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return reader{strings.NewReader(xml)}, nil }
}

// urlBaseFile opens a config.xml whose UrlBase is v.
func urlBaseFile(v string) func() (io.ReadCloser, error) {
	return file("<Config><Port>8080</Port><UrlBase>" + v + "</UrlBase></Config>")
}

// missing is a config.xml that cannot be opened.
func missing() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF }

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

type reader struct{ io.Reader }

func (reader) Close() error { return nil }

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
		file    func() (io.ReadCloser, error)
		want    string
		wantErr []string
	}{
		{name: "env wins", getenv: env("fromenv"), file: keyFile, want: "fromenv"},
		{name: "env needs no file", getenv: env("fromenv"), file: missing, want: "fromenv"},
		{name: "file alone", getenv: env(""), file: keyFile, want: "fromfile"},
		{name: "file with white space around the key, as Sonarr trims it", getenv: env(""), file: file("<Config><ApiKey>\n  fromfile \t</ApiKey></Config>"), want: "fromfile"},
		{name: "file with white space alone is no key", getenv: env(""), file: file("<Config><ApiKey> \n </ApiKey></Config>"), wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
		{name: "neither", getenv: env(""), file: missing, wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
		{name: "no key in file", getenv: env(""), file: func() (io.ReadCloser, error) {
			return reader{strings.NewReader(`<Config></Config>`)}, nil
		}, wantErr: []string{"SONARR__AUTH__APIKEY", "config.xml"}},
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
