// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Command probe is the acceptance suite's own probe: what a test needs
// done inside a container that divbast's core probe does not do. The
// harness builds it static, for the image's architecture, and mounts it
// read-only at /divbast/probe/app. A test runs it as a container's
// entrypoint, with a volume mounted at /config, to put the volume in the
// state it needs before the image itself mounts it: the image has no shell
// to do it with.
//
// Usage:
//
//	probe root-own                make /config root's, mode 0755, holding a file of root's
//	probe write <name> <content>  write content to /config/<name>, as the container's user
//	probe mkdir <name>            make the directory /config/<name>, as the container's user
//
// It prints nothing and exits 0 once it has done what it was asked. It
// prints why on stderr and exits 1 when it could not, and exits 2 for a
// command line it does not know.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run does what args say, and returns the exit status.
func run(args []string, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "root-own":
		err = rootOwn()
	case len(args) == 3 && args[0] == "write":
		err = write(args[1], args[2])
	case len(args) == 2 && args[0] == "mkdir":
		err = mkdir(args[1])
	default:
		_, _ = fmt.Fprintln(stderr, "usage: probe root-own | probe write <name> <content> | probe mkdir <name>")
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "probe:", err)
		return 1
	}
	return 0
}

// dir is where the probe works: the image's data directory, where a test
// mounts the volume.
const dir = "/config"

// write writes content to the file name in dir, which name cannot leave.
func write(name, content string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(name, []byte(content), 0o600)
}

// mkdir makes the directory name in dir, which name cannot leave.
func mkdir(name string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Mkdir(name, 0o700)
}

// keepName is the file root-own leaves in the directory. Docker copies
// what the image holds at a volume's path into the volume, with the
// directory's owner and mode, each time it makes a container that mounts
// the volume while the volume is empty: an empty volume would be the
// image's uid 65532's again on the next mount. Podman copies, and chowns,
// on a volume's first mount alone, which root-own's own container makes.
const keepName = "root-owned"

// rootMode is the mode of a directory root makes, which uid 65532 can
// enter and read, and not write.
const rootMode fs.FileMode = 0o755

// rootOwn makes dir uid and gid 0's, mode 0755, leaves keepName in it,
// and checks it reads back so.
func rootOwn() error {
	if err := os.WriteFile(filepath.Join(dir, keepName), []byte("left by the acceptance suite\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, rootMode); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return errors.New(dir + ": no owner to read")
	case st.Uid != 0 || st.Gid != 0 || fi.Mode().Perm() != rootMode:
		return fmt.Errorf("%s is %d:%d, mode %o, after root-own", dir, st.Uid, st.Gid, fi.Mode().Perm())
	}
	return nil
}
