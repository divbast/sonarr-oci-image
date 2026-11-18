// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Command divbast-sonarr is the image's health check and version.
//
// Usage:
//
//	divbast-sonarr health   check Sonarr answers /ping and is the pinned version
//	divbast-sonarr version  print the version
//
// health prints nothing when both halves hold, and one "ping: <reason>" or
// "status: <reason>" line for each that does not.
//
// Exit codes:
//
//	0  healthy, or the version printed
//	1  a half of health did not hold
//	2  the command line is wrong
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/divbast/sonarr-oci-image/internal/probe"
)

// version is set at build time, with -ldflags "-X main.version=…".
var version = "dev"

// within bounds each half of health, so both fit the HEALTHCHECK's 10 s.
const within = 4 * time.Second

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "health":
		os.Exit(health())
	case "version":
		fmt.Println("divbast-sonarr", version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: divbast-sonarr health|version")
	os.Exit(2)
}

func health() int {
	configXML := readConfigXML()
	key, err := probe.APIKey(os.Getenv, configXML)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	return probe.Probe(context.Background(), probe.Options{
		Base:    probe.Base(os.Getenv, configXML),
		APIKey:  key,
		Version: version,
		Within:  within,
	}, os.Stderr)
}

// readConfigXML reads /config/config.xml, confined to /config, once, and
// returns what opens what it read, or gives why it could not, each time it
// is called: the API key and the URL base both come from that one read.
func readConfigXML() func() (io.ReadCloser, error) {
	b, err := readFile()
	return func() (io.ReadCloser, error) {
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

// readFile reads /config/config.xml, confined to /config.
func readFile() ([]byte, error) {
	root, err := os.OpenRoot("/config")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile("config.xml")
}
