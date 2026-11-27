// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package probe is the image's health check: it asks Sonarr whether it
// answers, and whether it is the build the image pins.
package probe

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Options says where Sonarr is and what it must answer.
type Options struct {
	// Base is the URL up to and including the URL base, with no trailing /.
	Base string
	// APIKey is sent as X-Api-Key. It works under every authentication
	// method, so the probe needs no login.
	APIKey string
	// Version is the Sonarr version the image pins.
	Version string
	// Within bounds each half, ping and status, separately.
	Within time.Duration
}

// Probe checks Sonarr in two halves, neither of which implies the other.
// ping: GET <Base>/ping answers 200 with {"status":"OK"}, which proves
// the server and its database answer. status: GET
// <Base>/api/v5/system/status with the API key answers 200 for appName
// Sonarr at Version, which proves the build is the pinned one. It prints
// nothing and returns 0 when both hold. Otherwise it prints one
// "ping: <reason>" or "status: <reason>" line to stderr for each half that
// failed, the only record of why, and returns 1.
func Probe(ctx context.Context, o Options, stderr io.Writer) int {
	code := 0
	if err := get(ctx, o, "/ping", "", func(body []byte) error {
		var p struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("body is not JSON: %w", err)
		}
		if p.Status != "OK" {
			return fmt.Errorf("status is %q, want %q", p.Status, "OK")
		}
		return nil
	}); err != nil {
		_, _ = fmt.Fprintln(stderr, "ping:", err)
		code = 1
	}
	if err := get(ctx, o, "/api/v5/system/status", o.APIKey, func(body []byte) error {
		var s struct {
			AppName string `json:"appName"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &s); err != nil {
			return fmt.Errorf("body is not JSON: %w", err)
		}
		if s.AppName != "Sonarr" {
			return fmt.Errorf("appName is %q, want %q", s.AppName, "Sonarr")
		}
		if s.Version != o.Version {
			return fmt.Errorf("version is %q, want %q", s.Version, o.Version)
		}
		return nil
	}); err != nil {
		_, _ = fmt.Fprintln(stderr, "status:", err)
		code = 1
	}
	return code
}

// get sends GET <Base><path> within o.Within, with the API key if there is
// one, and returns what check makes of a 200 body, or why there was none.
func get(ctx context.Context, o Options, path, apiKey string, check func(body []byte) error) error {
	ctx, cancel := context.WithTimeout(ctx, o.Within)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Base+path, http.NoBody)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s answered %d, want 200", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return check(body)
}

// Base is where Sonarr listens, as the probe reaches it: loopback on
// SONARR__SERVER__PORT (8080 if unset), then the URL base with a leading
// / and no trailing /, or nothing. The URL base is
// SONARR__SERVER__URLBASE if set, else the UrlBase in config.xml, which
// configXML gives as ReadConfig does and the UI's Settings write, as
// Sonarr takes it. No config.xml gives none, so a setup that sets
// everything in the environment needs no config.xml. A config.xml that
// cannot be read or decoded is an error: guessing it has no URL base
// would send the probe where Sonarr only redirects.
func Base(getenv func(string) string, configXML func() ([]byte, error)) (string, error) {
	port := getenv("SONARR__SERVER__PORT")
	if port == "" {
		port = "8080"
	}
	u := getenv("SONARR__SERVER__URLBASE")
	if u == "" {
		c, err := readConfig(configXML)
		if err != nil {
			return "", fmt.Errorf("cannot tell the URL base: SONARR__SERVER__URLBASE is unset and %w", err)
		}
		if c != nil {
			// Sonarr trims a config.xml value's white space as it reads it
			u = strings.TrimSpace(c.URLBase)
		}
	}
	base := ""
	if u = strings.Trim(u, "/"); u != "" {
		base = "/" + u
	}
	return "http://127.0.0.1:" + port + base, nil
}

// APIKey is SONARR__AUTH__APIKEY if set, else the ApiKey in config.xml,
// which configXML gives as ReadConfig does, trimmed of white space as
// Sonarr trims it. Neither is an error naming both.
func APIKey(getenv func(string) string, configXML func() ([]byte, error)) (string, error) {
	if k := getenv("SONARR__AUTH__APIKEY"); k != "" {
		return k, nil
	}
	c, err := readConfig(configXML)
	if err != nil {
		return "", fmt.Errorf("no API key: SONARR__AUTH__APIKEY is unset and %w", err)
	}
	if c == nil {
		return "", errors.New("no API key: SONARR__AUTH__APIKEY is unset and there is no config.xml")
	}
	// Sonarr trims a config.xml value's white space as it reads it, and
	// compares the key it is sent with what is left
	k := strings.TrimSpace(c.APIKey)
	if k == "" {
		return "", errors.New("no API key: SONARR__AUTH__APIKEY is unset and config.xml has no ApiKey")
	}
	return k, nil
}

// configReads is how many times ReadConfig reads a config.xml that does
// not decode, and configReadGap how long it waits between two reads. The
// waits come to 160 ms at most, which the HEALTHCHECK's 2 s to spare over
// the probe's own budget covers.
const (
	configReads   = 5
	configReadGap = 40 * time.Millisecond
)

// ReadConfig returns config.xml as read gives it, once it decodes.
// Sonarr rewrites config.xml in place as it starts, truncating it and
// then writing it, so a read in between finds it empty or cut short:
// ReadConfig reads it again, up to configReads times in all, configReadGap
// apart, while what it finds does not decode. No config.xml, which read
// reports as fs.ErrNotExist, is nil and no error, since a setup that sets
// everything in the environment needs none. Any other error from read
// ends it at once, as one that starts "config.xml is unreadable:". A
// config.xml still invalid at the last read, or when ctx ends the wait
// for the next, is an error that starts "config.xml is invalid:".
func ReadConfig(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	var invalid error
	for n := range configReads {
		if n > 0 {
			wait := time.NewTimer(configReadGap)
			select {
			case <-ctx.Done():
				wait.Stop()
				return nil, fmt.Errorf("config.xml is invalid: %w; stopped reading it again: %w", invalid, context.Cause(ctx))
			case <-wait.C:
			}
		}
		b, err := read()
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("config.xml is unreadable: %w", err)
		}
		if invalid = xml.Unmarshal(b, new(fileConfig)); invalid == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("config.xml is invalid: %w", invalid)
}

// fileConfig is what the probe reads from config.xml.
type fileConfig struct {
	APIKey  string `xml:"ApiKey"`
	URLBase string `xml:"UrlBase"`
}

// readConfig decodes the config.xml configXML gives, nil when it gives
// none, or says why it could not.
func readConfig(configXML func() ([]byte, error)) (*fileConfig, error) {
	b, err := configXML()
	if err != nil || b == nil {
		return nil, err
	}
	var c fileConfig
	if err := xml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config.xml is invalid: %w", err)
	}
	return &c, nil
}

// noRedirects reports a redirect as its status instead of following it:
// Sonarr redirects a request that lacks its URL base, which is the
// misconfiguration the probe exists to show.
var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
