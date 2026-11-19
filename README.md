<!-- SPDX-License-Identifier: GPL-3.0-only -->
<!-- SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee> -->

# sonarr-oci-image

[Sonarr](https://github.com/Sonarr/Sonarr), the TV series manager, in an
image that runs at zero Linux capabilities, with no new privileges, on a
read-only root filesystem as a non-root user, with no shell in it. It is
built from Sonarr's source, and goes through divbast/ci's shared pipeline:
lint, a build for amd64 and arm64, the confinement contract and its own
acceptance suite under Docker and rootless Podman, a vulnerability scan,
and a daily publish to `ghcr.io/divbast/sonarr`.
[Publishing](#publishing) says when it publishes and under which tags.

**This is Sonarr v5, upstream's beta.** Sonarr's stable line, v4, runs on
.NET 6, out of support since 2024, so the image follows v5's develop
line, on .NET 10, until upstream publishes v5 stable, and then moves to
stable. [The develop line](#the-develop-line) says what that means for
you: nothing to do but expect a beta's changes.

## Base

The image is a Sonarr v5 develop release, built from source: the version
and commit the Containerfile's `SONARR_VERSION` and `SONARR_COMMIT` name,
which are the release and the commit its tag names. It runs on Docker
Hardened Images' [`aspnetcore`](https://hub.docker.com/hardened-images/catalog)
for Debian 13: the .NET runtime, ICU and OpenSSL, no shell and no package
manager. The app is built framework-dependent, so rebuilding the image
patches the runtime. Every stage of the build is also a DHI image, and CI
refuses to build until Docker's signature on each is verified:

- `fetch` checks out the commit and asserts the release's tag still names
  it.
- `frontend` builds the UI with yarn and Vite, from `yarn.lock`, which pins
  every package by its hash, and runs no package's install script.
- `backend` publishes the .NET app, with NuGet checking the signature of
  each package. Upstream keeps no lock file and pins every package by an
  exact version from nuget.org alone, so there is none here. NuGet's
  advisories stay warnings in the build log, where upstream makes them
  errors: the scan already blocks a vulnerable package the image does not
  ship yet, and an error would hold back every other update until upstream
  moved.
- `probe` builds the health probe, a static Go binary on the standard
  library alone.

Sonarr is never patched. The one change the build makes to an upstream
file is to `global.json`, in the backend stage, so that any later .NET SDK
of the same major and minor can build it; that file does not ship.

## Container Features

| Property | Value |
|---|---|
| Capabilities | none (`--cap-drop ALL`) |
| Privileges | no new ones (`--security-opt no-new-privileges`), under the engine's seccomp filter |
| Root filesystem | read-only |
| User | uid/gid 65532 (`nonroot`) |
| Port | 8080 (the UI, the API and `/ping`), and nothing else listens |
| tmpfs required | `/tmp`, which nothing can run from (`--tmpfs /tmp:size=64m,noexec,nosuid,nodev`); [size it](#tmp) to your database |
| Volume | `/config`, which you mount, owned by 65532; and your [media and downloads](#media-and-hardlinks) |
| Processes | one: `Sonarr`, as PID 1, which runs `ffprobe` on media files |
| Shell in image | none |
| Package manager | none |
| Outbound network | none needed to serve or be healthy; [Sonarr reaches out at rest](#egress-at-rest) |
| Healthy | the shipped probe: `/ping` 200 **and** `GET /api/v5/system/status` reporting Sonarr at this image's version |

## Running

Pull `ghcr.io/divbast/sonarr:latest`, which follows Sonarr's v5 develop
releases until v5 stable, as [The develop line](#the-develop-line) says; a
version tag, `:<version>` with the Sonarr release in place of `<version>`,
pins one. The package page on ghcr.io lists the tags. To build it
yourself: `docker login dhi.io`, since every image the build starts from
is Docker Hardened, then `just setup`, then `ENGINE=podman just build`,
which tags it `localhost/sonarr:dev`. Substitute that tag for
`ghcr.io/divbast/sonarr:latest` in the command below.

```bash
podman run -d --name sonarr \
  --cap-drop ALL --read-only --read-only-tmpfs=false \
  --security-opt no-new-privileges \
  --user 65532:65532 \
  --tmpfs /tmp:size=64m,noexec,nosuid,nodev \
  -v sonarr-config:/config \
  -v /srv/data:/data \
  -p 127.0.0.1:8080:8080 \
  --health-cmd '["/usr/local/bin/divbast-sonarr","health"]' \
  ghcr.io/divbast/sonarr:latest
```

`--read-only-tmpfs=false` and `--health-cmd` are Podman's alone. Without
the first, Podman mounts writable tmpfs of its own at `/run`, `/var/tmp`
and elsewhere beside the read-only root; without the second, it never
runs the image's health check, as [Health](#health) says. Docker takes
the same command without them, and runs the image's `HEALTHCHECK` itself.

Sonarr serves its UI and API on 8080. **It asks for a login from the
start:** a first start on an empty `/config` creates the user `sonarr`
with a random password, and logs it, once, as `Authentication is enabled
and no user existed, created 'sonarr' with the password: …`. Read it with
`podman logs sonarr` (or `docker logs`), sign in, and change it under
Settings, General. To choose the first password yourself, set
`SONARR__AUTH__INITIALPASSWORD` for the first start, as
[Authentication](#authentication) says. Where Podman logs to journald, as
it does by default under systemd, journald's rate limit can drop much of a
first start's log, which lists every database migration, and the password
line with it: set the password, or run with `--log-driver k8s-file`. A
stop (SIGTERM) ends Sonarr well
inside the engine's timeout, with exit 0. Restarting it from its UI
happens inside the same process, so the container keeps running.

### `/config`

The image carries an empty `/config` owned by 65532, and declares no
`VOLUME`. A new named volume takes its owner from it on first mount, so
`-v sonarr-config:/config` just works. Sonarr keeps everything it writes
there: `config.xml`, its databases, logs, backups and posters.

- A `/config` that 65532 cannot write, such as a bind mount root made, or a
  volume an image run as root left, makes Sonarr exit 3 at once, with
  `Sonarr failed to start: AppFolder /config is not writable` in its log.
  Give the directory, and everything in it, to 65532 instead of running
  the image as root: `chown -R 65532:65532 <dir>` as root, or under
  rootless Podman `podman unshare chown -R 65532:65532 <dir>`. For a Podman
  bind mount, the `:U` option does the same at start: `-v ./config:/config:U`.
- On an SELinux host, such as Fedora or RHEL, a bind mount also needs
  Podman to relabel it, or the container cannot use it: add `:Z`, as in
  `-v ./config:/config:U,Z`. `/config` is Sonarr's alone, so the private
  label `Z` fits it.
- With no mount at all, the read-only root refuses Sonarr's writes, and it
  exits 3 with the same line, but only after about two minutes, without
  ever listening: the mistake shows, slowly. Nothing would have been kept.
- A startup failure Sonarr calls non-recoverable, such as a PID file it
  cannot write, exits 3 too. The image passes `-exitimmediately`; without
  it Sonarr waits an hour for a key press no one can make in a container.

### Coming from another image

**Back up first.** Sonarr v5 migrates a v4 database as it opens it, and no
v4 opens it again: keep the old `/config`, or a backup from the old
install's Settings, System, Backup, until you are sure.

Copy the old `/config` into the new volume or directory, then give it all
to 65532 as above, with `chown -R`: the files another image wrote are
usually its PUID's, such as 1000. Or start fresh, sign in, and restore the
backup under System, Backup; the restore applies when Sonarr restarts,
which it does in the same container.

- This image serves on 8080, not Sonarr's usual 8989, and its
  `SONARR__SERVER__PORT` wins over the old `config.xml`. Whatever reaches
  Sonarr by its address, Prowlarr's Sonarr application among them, needs
  the new one, or the old number published to the new port
  (`-p 127.0.0.1:8989:8080`).
- An old install with authentication `None` meets the login: v5 takes
  `None` only from `SONARR__AUTH__METHOD`, makes the user `sonarr` if none
  existed, and logs its password, as in [Running](#running).
- Your media and download folders must be mounted at the paths the old
  install used, or remapped in Sonarr, and writable by 65532:
  [Media and hardlinks](#media-and-hardlinks).

### `/tmp`

`/tmp` is the contract's one tmpfs, and Sonarr uses it to stage a backup,
copying the database there before it zips the backup into `/config`, and
to receive a backup you restore. So `/tmp` must hold at least twice
`sonarr.db`, or the backup, the weekly scheduled one included, fails.

64 MiB, the size above and the one the acceptance suite runs under, is far
more than a new install needs: a backup of a database holding 50 tags
fitted in 1 MiB. A library of thousands of episodes makes a far larger
database. To size it for your own, read the size of `sonarr.db` in the
`/config` volume, with `ls -l` on the host path `docker volume inspect` or
`podman volume inspect` gives, double it and keep to 64 MiB or more. For
example, a 300 MiB database wants `--tmpfs /tmp:size=640m,noexec,nosuid,nodev`.
A tmpfs counts against the container's memory, and a backup fills it only
while it runs, so size it for the backup and no further.

## Configuration

Any `config.xml` key can be set as `SONARR__<SECTION>__<KEY>`, and the
environment wins over the file. The image sets `SONARR__SERVER__PORT=8080`,
which also wins over a `config.xml` brought from another image that says
8989. The keys most needed:

| Variable | What it does |
|---|---|
| `SONARR__AUTH__INITIALPASSWORD` | The first user's password, on a first start, in place of one Sonarr makes and logs; `SONARR__AUTH__INITIALUSERNAME` names the user, `sonarr` by default |
| `SONARR__AUTH__METHOD` | `Forms`, upstream's default; `External`, behind a proxy that authenticates; `None` turns authentication off, from the environment alone ([Authentication](#authentication)) |
| `SONARR__AUTH__REQUIRED` | `Enabled`, `DisabledForLocalAddresses` or `DisabledForLocalhost` |
| `SONARR__AUTH__APIKEY` | The API key, over the one in `config.xml` |
| `SONARR__SERVER__URLBASE` | The path a reverse proxy serves Sonarr under, such as `/sonarr` |
| `SONARR__SERVER__TRUSTEDNETWORKS` | Comma-separated networks, in CIDR form, whose `X-Forwarded-For`, `-Proto` and `-Host` headers Sonarr trusts |
| `SONARR__LOG__ANALYTICSENABLED` | `false` turns [analytics](#analytics) off |
| `SONARR__POSTGRES__HOST`, `__PORT`, `__USER`, `__PASSWORD`, `__MAINDB`, `__LOGDB` | [PostgreSQL](#postgresql) in place of SQLite |

### Authentication

Sonarr v5 asks for a login from the start: its method is `Forms`, and a
first start makes the user `sonarr`, with the password from
`SONARR__AUTH__INITIALPASSWORD`, or else one it makes and logs once. Keep
the port on the host's loopback, as [Running](#running) does, until you
have signed in and set the password you want.

Sonarr does not take `None` from `config.xml` any more: it rewrites a
`None` there to `Forms`. `SONARR__AUTH__METHOD=None` still turns
authentication off, and lets in anyone who can reach the port, who can then
download a backup, which holds `config.xml` and so the API key. Set
`External` instead when Sonarr sits behind a proxy that authenticates every
request itself: `External` makes Sonarr let in everyone who reaches it, so
nothing but that proxy may. A forgotten password can be reset from the
login page: Sonarr writes a one-use token to its log.

The probe does not mind which method is in force: it sends the API key,
which every method accepts.

### Behind a reverse proxy

Set `SONARR__SERVER__TRUSTEDNETWORKS` to the proxy's address or network,
such as `172.18.0.0/16`, so that Sonarr takes the client's address and
scheme from the headers the proxy sends, and not the proxy's own. Without
it, every request looks to be from the proxy.

**Do not use `SONARR__AUTH__REQUIRED=DisabledForLocalAddresses` behind a
proxy** unless the proxy sets `X-Forwarded-For` and is listed in
`SONARR__SERVER__TRUSTEDNETWORKS`. Sonarr treats a client as local when
its address is local and the request carries no `X-Forwarded-For` header.
A proxy on a private network that sends none makes every proxied client
local, and authentication is skipped for everyone who can reach the proxy.
`SONARR__AUTH__TRUSTCGNATIPADDRESSES=true` counts 100.64.0.0/10, such as
Tailscale's, as local too, and has the same edge.

If the proxy serves Sonarr under a path, set the URL base, in the
environment or in the UI. Sonarr answers nothing at the bare path but a
redirect. The health probe reads the base from the environment, else from
`config.xml`, so either works, and it never follows a redirect.

### Analytics

Analytics are on, as upstream ships them. They and the error reports
Sonarr sends to Sentry go to Servarr. Turn them off in the UI, under
Settings, General, or with `SONARR__LOG__ANALYTICSENABLED=false`.

### Updates

Sonarr's own updater is off. The image's `/app/package_info` names Docker
as the update mechanism, on the `develop` branch, so the UI offers no
update, and a `config.xml` brought from another install that says
`BuiltIn` or `main` is switched at start. Sonarr's health page may still
say a newer build exists on `develop`: it arrives as a new image, and you
update by pulling it.

## The develop line

Upstream publishes every Sonarr v5 build as a GitHub pre-release, and has
not dated v5 stable. Renovate moves the image to each new v5 develop
release, once the acceptance suite passes on it, and the daily slot
publishes it as `:latest` and its version, such as `:5.0.0.2997`. It is a
beta: expect a beta's changes, and keep backups.

When upstream publishes v5 stable, the image stops taking develop builds
and stays on the last one before it, which the stable release carries,
until it moves to stable: a check holds Renovate's pull request red, and
the move is made by hand, so that no database ever meets an older Sonarr
than the one that migrated it. Then `:latest` follows Sonarr's stable
releases, and you change nothing.

## Egress at rest

The contract's no-egress check holds: the image serves and is healthy on a
network with no route out. Sonarr, though, reaches out while it runs, as
upstream ships it:

- `services.sonarr.tv`, for upstream's notices, the time, update checks and
  scene mappings, and `thexem.info`, for scene numbering. With no route
  out, Sonarr logs `Failed to retrieve notifications` and `Unable to verify
  system time`, and carries on.
- `skyhook.sonarr.tv`, for series metadata, and the images it names, which
  Sonarr keeps under `/config/MediaCover`. Without it, no series can be
  added or refreshed.
- Analytics and error reports go to Servarr, until turned off.
- Then whatever indexers, download clients, lists and connections you
  configure.

None of it is needed to serve or to be healthy. One thing does change
with no route out, once the image is more than 14 days older than the
Sonarr release it was built from, as a published image soon is: Sonarr
then checks for an update with every round of its health checks, the
check fails, and the round shows nothing, so the UI's System, Status page
is empty rather than listing the warnings it would. The probe stays
healthy; give Sonarr a route to `services.sonarr.tv` to see the page.

## Not supported

There is no shell in the image, so these cannot work:

- Custom Script connections.
- Import Using Script.
- The Script update mechanism.
- Any other interpreted script.

A static binary on a mount still runs, unless you mount it from somewhere
`noexec`. `ffprobe`, which Sonarr runs to read media files, is a static
binary in the image itself.

## Media and hardlinks

Sonarr imports what your download client finishes into your library, so
it needs both, mounted and writable by 65532. Hardlinking, which imports
instantly and without a second copy, needs the source and the destination
on one filesystem, so give Sonarr and the download client one shared
`/data` mount, such as `/data/downloads` and `/data/tv` beside each other,
not a mount each. Under `--cap-drop ALL`, the kernel's protected-hardlinks
rule lets a process link only a file it owns or can read and write; when
65532 can do neither, Sonarr copies the file instead, so run the download
client as 65532 too, or give 65532 its files. On an SELinux host, a
`/data` bind mount several containers share takes Podman's `:z`, the
shared label, not `:Z`, which would keep it to one container.

## PostgreSQL

Sonarr can use PostgreSQL through the `SONARR__POSTGRES__*` variables
above. This image has not been tested with it.

## Health

A server that answers can still be broken, so the image ships its own probe
and declares a `HEALTHCHECK`: `--interval=30s --timeout=10s
--start-period=300s --retries=3`. The start period is long because
Sonarr answers nothing until its database migrations finish, and a v4
database meeting v5, or an upgrade's on a large library, can be slow; the
first check that passes ends it, so a healthy start loses nothing. Docker
runs the check with no configuration.

Podman does not. Podman's `libimage` reads a healthcheck only out of a
Docker-media-type manifest, and divbast images are published as an OCI
index, so Podman's inspection path never looks at the config's healthcheck
field — [podman/podman#25454](https://github.com/containers/podman/issues/25454)
and [#18904](https://github.com/containers/podman/issues/18904) track it. A
Podman user passes it explicitly, with `--health-cmd` as in
[Running](#running). Under a Quadlet, the equivalent is:

```
HealthCmd=["/usr/local/bin/divbast-sonarr","health"]
```

The array above has no leading `"CMD"` on purpose: Podman before 5.8.0
re-splits an array that starts with `"CMD"` into one useless token, so
omitting it is what makes the check work across versions. Podman takes
none of the image's timings either, and by default allows no start
period: add `--health-start-period=300s` (`HealthStartPeriod=300s` in a
Quadlet) if migrations could outlast three of its checks.

| | |
|---|---|
| `/usr/local/bin/divbast-sonarr health` | The in-container probe `HEALTHCHECK` runs. Asserts that `/ping` answers 200 with `{"status":"OK"}` **and** that `GET /api/v5/system/status`, with the API key, says `appName` is `Sonarr` and `version` is this image's. Prints nothing when healthy; otherwise one line, `ping: <reason>` or `status: <reason>`, for each half that failed, and exits 1 |
| `/usr/local/bin/divbast-sonarr version` | Prints the version |

`/ping` runs a database query, so it shows the server and its database
answer; only the status shows the build is the pinned one. The probe reads
the API key from `SONARR__AUTH__APIKEY`, else `/config/config.xml`, and
fails with a line saying so when it finds neither. Sonarr makes a key for
itself on its first start, so a plain run needs nothing set.

There is no shell in this image, so a health command given as a plain
string, which Docker and Podman both run through `/bin/sh -c`, can never
pass.

## The pipeline

Three workflows, each a short caller of divbast/ci's reusable workflow of
the same purpose, at one pinned release of it:

- `build.yml` calls `app-check.yml` on every push, pull request, daily slot
  and button run: lint, the image built for amd64 and arm64 once every
  image the Containerfile builds from is verified, the acceptance suite
  against each under Docker and rootless Podman, and the scan. Its
  `required` job is the one check `main`'s ruleset requires. From the daily
  slot and the button, on `main`, it then calls `app-publish.yml`.
- `renovate.yml` runs Renovate, as below.
- `stall.yml` fails, and GitHub emails you, when a Renovate pull request
  has been open more than 14 days or red or pending for more than a day, or
  when Renovate has not succeeded in a day.

The acceptance suite, on top of the contract, checks that Sonarr serves
`/ping` and its login page, sends a visitor with no session to log in,
refuses the API without a key, signs in with the first password it was
given or logs the one it made, stops on SIGTERM and exits 0, keeps its
state across a restart, restarts from its UI inside the process, reports
Docker as its updater on `develop`, is healthy under every authentication
method and URL base, refuses an unwritable or missing `/config`, exits on a
startup failure, runs on a `/config` a v4 image left, makes and serves a
backup within the `/tmp` it is given, restores one through the UI's
upload, and runs `ffprobe`.

### Publishing

A merge never publishes. The daily slot publishes `main` when `main` moved
since the commit the published `:latest` was built from, and the button, a
manual run of `build` from `main`, publishes at once.

Its publish job calls divbast/ci's `app-publish.yml` with `image:
ghcr.io/divbast/sonarr`. A publish pushes an index of the amd64 and arm64
images, attests it, verifies the attestation, and only then tags it twice:
with Sonarr's version, the Containerfile's `SONARR_VERSION`, and with
`latest`. It ends by checking, as an anonymous client, that both tags are
the attested index.

## Dependency updates

Every input the Containerfile names is pinned by digest: the DHI runtime,
and the DHI Node, .NET SDK and Go images the stages build on. Sonarr's own
version and commit are pinned together in two adjacent `ARG`s. The probe
is the standard library alone, so `go.mod` requires nothing but
divbast/ci's harness for its tests.

A pin only moves through a pull request, and
[Renovate](https://docs.renovatebot.com/) opens those. It runs every
morning, after every merge to `main`, whenever CI finishes on one of its
branches and when the Dependency Dashboard issue is edited, from
`.github/workflows/renovate.yml`, as the divbast org's `divbast-renovate`
GitHub App. Its settings are divbast/renovate-config's shared presets
(`:app` among them), and `renovate.json` adds what they cannot know: where
Sonarr's version lives, and the watch for v5 stable. The pull requests
Renovate opens here are:

- A new Sonarr v5 develop release moves `SONARR_VERSION` and
  `SONARR_COMMIT` together. Renovate follows Sonarr's GitHub releases (the
  `github-releases` datasource), pre-releases included, and never its tags,
  among which Sonarr's repository also holds Radarr's. The commit is the one
  the release's tag names, and the build fails if the tag ever names
  another.
- Upstream's first v5 stable release moves the Containerfile's
  `SONARR_STABLE_V5` line, in the same pull request as the develop line's
  pins, which a check then holds red, and which never merges itself:
  [The develop line](#the-develop-line).
- A new DHI image arrives as a pull request that moves its digest, and CI
  verifies Docker's signature before it builds. The .NET SDK and the
  ASP.NET Core runtime move together in one pull request, and a new .NET
  major waits for approval on the Dependency Dashboard.
- A release of divbast/ci arrives as one pull request that moves every
  workflow's pin of it together.
- The toolchain in `mise.toml` moves by pull requests of its own.

Renovate merges its own pull requests once every check on one has passed,
while the repository variable `SONARR_AUTOMERGE` is `enabled`. Three
kinds first wait for someone to approve them on the Dependency Dashboard:
a new .NET major, a new major version of divbast/ci, and a raise of
`go.mod`'s `go` line.
Any other value of the variable, or none, stops merging from the next run,
and Renovate goes on opening and rebasing pull requests for a person to
merge:

    gh variable set SONARR_AUTOMERGE --body disabled

After a merge it rebases every open update the merge left behind, so each
is tested again against the new `main` before it merges. It merges with a
squash, which GitHub signs, because `main` accepts only signed commits.

The App, the `renovate` environment holding its key, `SONARR_AUTOMERGE`
and this repository's rulesets are all managed from
[divbast/infra](https://github.com/divbast/infra).

## Development

- `just setup` fetches divbast/ci into `.ci/` at the commit the workflows
  pin, then installs the pinned toolchain from `mise.toml`.
- `just build` builds the image locally, without verifying the images it
  builds from, and needs `docker login dhi.io`; `just ci-tools &&
  .ci/bin/divbast-verify-images` does verify them, with cosign from
  `mise.toml`.
- `just scan` reads the published image with
  [skopeo](https://github.com/containers/skopeo), which must be on `PATH`;
  so must it for `just ci`. CI runners already have it.
- `just test` builds, then runs the acceptance suite against the image,
  under `ENGINE`, docker unless it says podman. Under Docker it needs
  `zstd` on `PATH`, since every layer of a Docker Hardened Image is zstd
  and the suite reads the image's layers through it; no `mise.toml` entry
  provides it. CI runners already have it, and a Mac gets it with `brew
  install zstd`.
- `just lint` runs every lint gate, and needs a running Docker daemon, for
  `docker build --check`.
- `just ci` runs every gate, the scan included. Run it before committing.

The recipes that call GitHub, pinact's and zizmor's, read a fine-grained
personal access token from `GITHUB_TOKEN`: divbast/ci's README says which,
and how to pass it.

## AI/LLM Disclosure

This project was developed with significant LLM involvement. Each git commit contains an `Assisted-by:` tag detailing the particular model/tool used.
