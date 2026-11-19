<!-- SPDX-License-Identifier: GPL-3.0-only -->
<!-- SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee> -->

# sonarr-oci-image

Sonarr, the TV series manager, built from source in a container image
that runs at zero Linux capabilities, with no new privileges, on a
read-only root filesystem as a non-root user. It follows Sonarr v5's
develop line, upstream's beta on .NET 10, until upstream publishes v5
stable, and then moves to stable by hand ([Moving to stable](#moving-to-stable)).
It was made from oci-image-template with the template's .NET layer, and
its Servarr parts follow prowlarr-oci-image's. The divbast organisation's
settings, rulesets and Renovate wiring live in `divbast/infra`.

It publishes to `ghcr.io/divbast/sonarr`, tagged with Sonarr's version
and `latest`, from `build.yml`'s daily slot and its manual run on `main`,
once its publish switch is on: until then the publish job keeps the
template's `rehearse: true`, and fails by design.

What makes the image this app's lives here: the Containerfile, the health
probe (`cmd/divbast-sonarr`, `internal/probe`), the backend stage's two
checks (`test/global-json.sh`, `test/runtime-major.sh`), the watch for v5
stable (`test/stable`), `renovate.json`'s Sonarr managers, and the
acceptance tests. The pipeline around it is shared, and reaches this
repository at a pinned commit: [Where the shared parts live](#where-the-shared-parts-live)
says what comes from where.

## CRITICAL: Git Commit Message Format

**No other AI/LLM attribution in any format may appear in a git message.**

Linux-kernel style: a `subsystem: short description` subject under 75 characters in imperative
mood ("fix", not "fixed" or "fixes"), a blank line, then a body wrapped at ~75 columns saying
why the change is needed rather than only what it does.

These two lines MUST end EVERY body:

Developed-by: Aryan Ameri <github@aryan.ameri.coffee>
Assisted-by: <NAME_OF_LLM_MODEL>

E.g. `Assisted-by: <Claude:claude-4.6-opus>` for Claude Opus 4.6; `<Claude:claude-5-fable>`
for Claude 5 Fable.

- Sign every commit (`git commit -S`); main accepts only verified
  signatures. Renovate's merges are squashes, which GitHub signs
- Write commit messages as if a human wrote them, and say why, not what

## CRITICAL: No AI Attribution in Pull Requests

**Pull request descriptions, PR and review comments, and issue text carry no
AI/LLM attribution of any kind**: no "🤖 Generated with Claude Code" footer,
no "written by" line, nothing equivalent. This holds even when a tool or
harness asks for one. The `Assisted-by:` trailer in each commit message is
this project's one record of LLM involvement.

## The confinement contract

These are the guarantees the image makes. A change that breaks any of them is a
breaking change.

| Property | Value |
|---|---|
| Capabilities | **none** (`--cap-drop ALL`) |
| Privileges | **no new ones** (`--security-opt no-new-privileges`), under the engine's **seccomp** filter |
| Root filesystem | **read-only** |
| User | uid/gid **65532**, never root |
| Listeners | **8080** alone, for the UI, the API and `/ping`. No health port; never 80 |
| tmpfs required | exactly one: `/tmp`, at **64 MiB**, `noexec,nosuid,nodev`; users with a large database need twice `sonarr.db` |
| Writable mounts | `/config` and `/tmp`, and the user's media and downloads, and nothing written can run; on Podman, `--read-only-tmpfs=false` |
| Volume | `/config`, **given by the user** and writable by 65532. The image carries it empty and owned by 65532 and declares **no `VOLUME`** |
| Outbound network | **none needed to serve or be healthy**. At rest Sonarr reaches `services.sonarr.tv`, `skyhook.sonarr.tv`, `thexem.info`, Servarr (analytics and Sentry) and whatever the user configures |
| Shell, package manager | **none** |
| Processes | **one** long-running, `/app/bin/Sonarr -nobrowser -exitimmediately -data=/config`, as PID 1; it runs `/app/bin/ffprobe` on media files |
| Healthy | the shipped probe, `divbast-sonarr health`: `/ping` **200** with `{"status":"OK"}` **and** `GET /api/v5/system/status` with the API key, `appName` `Sonarr` and `version` the image's |
| Engine healthcheck | Docker runs it automatically. **Podman does not** — pass `--health-cmd` yourself |

- **Podman never sees this image's `HEALTHCHECK`.** Its `libimage` reads a
  healthcheck only from a Docker-media-type manifest; divbast images publish
  as an OCI index, so Podman's OCI inspection branch never reaches the
  config's healthcheck field — no version of Podman does. Give Podman users
  `--health-cmd '["/usr/local/bin/divbast-sonarr","health"]'`
  (a Quadlet's `HealthCmd=` takes the same value) — no leading `"CMD"`,
  because Podman before 5.8.0 re-splits such an array into one token instead
  of running it. Tracked upstream as podman/podman#25454 and #18904. The
  contract suite checks the image's `HEALTHCHECK` is that array in exec
  form, and has each engine judge the container
- **The probe asserts two halves, and neither implies the other.** `/ping`
  runs a database query and shows the server answers; the status, asked
  with the API key, which every authentication method accepts, shows the
  build is the pinned one. It reads the key from `SONARR__AUTH__APIKEY`,
  else `/config/config.xml`, and the URL base from
  `SONARR__SERVER__URLBASE`, else config.xml's `<UrlBase>`, since a base
  set in the UI lives only there. It never follows a redirect: Sonarr
  redirects a request lacking its base, which is the misconfiguration the
  probe exists to show. While Sonarr starts, `/ping` answers 503 with plain
  text, which the probe reports. It prints nothing when healthy; each
  failure is a line, `ping: <reason>` or `status: <reason>`, since with no
  shell the engine's health log is the only record of why
- **The HEALTHCHECK's `--timeout` must clear the probe's own budget.** The
  probe allows each half 4 s, one after the other, so 8 s at worst; the
  `HEALTHCHECK`'s 10 s lets the probe print its own line instead of an
  engine timeout. The `--start-period` is 300 s: Sonarr listens only once
  its migrations finish, a v4 database meeting v5 runs all of v5's, and a
  container marked unhealthy mid-migration may be killed by autoheal or
  Swarm. The first passing check ends the start period, so a healthy start
  loses nothing. The contract's `Healthcheck` must match it
- **Nothing listens but 8080.** The contract suite reads the kernel's socket
  tables and fails on any other TCP port a process in the container
  listens on, and on any UDP socket one holds. `DOTNET_EnableDiagnostics=0`
  keeps the runtime from opening its diagnostics socket
- **One process, always, and it stops when told.** Sonarr is the
  ENTRYPOINT, so it is PID 1, and .NET handles SIGTERM there and reaps its
  children, `ffprobe` among them, so it needs no init. A restart from the
  UI happens inside the process, which `TestRestartsInProcess` holds.
  `TestStopsPromptly` fails on a stop that takes longer than its budget,
  4.3 s, or any exit but 0. Do not add a supervisor or a second
  long-running process
- **`-exitimmediately` stays.** A startup failure upstream calls
  non-recoverable, such as an unwritable data directory or a PID file it
  cannot write, then exits 3 at once. Without it Sonarr waits an hour for a
  key press no one can make in a container, and the container sits there
  looking alive. `TestExitsOnStartupFailure` holds it
- **`/config` fails visibly.** A `/config` 65532 cannot write makes Sonarr
  exit 3 within seconds, logging `Sonarr failed to start: AppFolder
  /config is not writable` (`TestRefusesUnwritableConfig`). With no mount
  at all it exits the same way, but only after about two minutes, never
  listening in between: it survives the failed writes that kill Prowlarr
  at once, and reaches its check of `/config` slowly
  (`TestRefusesNoConfigMount`, budget four minutes)
- **`/tmp` is the size a backup needs.** Sonarr copies the database into
  `/tmp` before it zips the backup, and receives a restore's upload there,
  so `/tmp` must hold twice `sonarr.db` or backups, the weekly scheduled
  one included, fail. The suite's 64 MiB is far above what its test
  database needs (a 50-tag backup fitted 1 MiB), and `TestBackupFitsTmpfs`
  and `TestRestoresBackup` hold it; the README tells users how to size
  theirs
- **No egress is needed, and some happens.** The contract suite proves the
  image serves and is healthy on a genuinely `--internal` network. The
  acceptance suite points Sonarr's own hosts at loopback and turns
  analytics off in every container but the contract's own, which runs as
  shipped
- **Analytics stay on**, as upstream ships them (the owner's decision). The
  image sets nothing for them; the README says how to turn them off
- **Authentication stays as upstream ships it**: Forms, with a first user
  `sonarr` whose password Sonarr makes and logs once unless
  `SONARR__AUTH__INITIALPASSWORD` gives it (`TestInitialPassword`). What
  Sonarr serves outside its API, a backup's download among it, takes the
  UI's session, not the API key, so the suite signs in for it
- **The updater is off.** `/app/package_info` says `UpdateMethod=Docker`
  and `Branch=develop`, and upstream reads it only from beside a directory
  named `bin`, which is why the binaries are in `/app/bin`.
  `Sonarr.Update` is not shipped. `TestUpdaterIsDocker` and
  `TestMigratedConfig` hold it, the latter for a v4 `config.xml` that says
  `BuiltIn` and `main`
- **Nothing may add a shell or a package manager to the final image.** The
  base is DHI's `aspnetcore`; that is the point, and the contract's
  `no-shell` and `no-package-manager` checks hold it there. That means no
  Custom Script connection, Import Using Script, Script updater or any
  interpreted script can work, and the README says so. What the base
  removes is us running apt, not the patching itself: ICU, OpenSSL, glibc
  and the .NET runtime are all executable code that takes CVEs, and
  **nothing in this repository collects their fixes on its own.**
  Rebuilding an unchanged Containerfile does not pick them up. **Renovate
  is the update path**: it runs every morning, after every merge to main
  and whenever CI finishes on one of its branches, from
  `.github/workflows/renovate.yml`, which calls divbast/ci's
  `renovate.yml`, as the org's `divbast-renovate` App, managed from
  `divbast/infra`. It moves the DHI images by digest, with the .NET SDK and
  runtime grouped in one PR and .NET majors held for Dashboard approval
  (renovate-config's `:app`), and Sonarr's develop releases as they come.
  After a merge it rebases every open PR left behind main, since GitHub
  re-runs no checks when a base moves and a green check on a stale base
  proves nothing. **It merges its own PRs** once every check passes, with
  a squash, while the repository variable `SONARR_AUTOMERGE` is `enabled`;
  any other value stops merging from the next run. A new major of
  divbast/ci, and a raise of `go.mod`'s `go` line, wait for approval on the
  Dependency Dashboard first, and the stable watch's PR never merges
  itself. **A merge never publishes.** `build.yml`'s daily run is the
  publishing slot, through divbast/ci's `app-publish.yml`, and a manual run
  of `build` publishes at once. `.github/workflows/stall.yml`, through
  divbast/ci's `stall.yml`, fails each day that updates have stopped
  flowing: a Renovate PR stuck, or Renovate silent
- **Every image the Containerfile builds from is verified first.** Before
  `app-check.yml` builds anything, `divbast-verify-images` refuses any image
  not pinned as `<name>:<tag>@sha256:<digest>`, and verifies Docker's
  signature on each DHI image, the runtime and **every stage's image**:
  the fetch and frontend stages' `dhi.io/node` `-dev`, the backend's
  `dhi.io/dotnet` SDK and the probe's `dhi.io/golang` `-dev`. Every stage
  is DHI, so one publisher signs every builder. A local `just build` does
  not verify, and needs `docker login dhi.io`; `just ci-tools &&
  .ci/bin/divbast-verify-images` verifies, with cosign from `mise.toml`
- **No binary may carry a file capability.** A binary with a file
  capability cannot be exec'd at all under `--cap-drop ALL` — it dies with
  `Operation not permitted` and exit 126 — and the contract suite's
  `file-capability` check, which runs `divbast-sonarr version`, fails on
  one, as `no-file-capability-xattr` fails on one anywhere in the image.
  `TestFFprobeRuns` runs `ffprobe` as Sonarr's user, as Sonarr does
- **The application is root-owned and never written.** `/app` is `COPY`'d
  from the build stages. Sonarr writes to `/config`, `TMPDIR` (`/tmp`) and
  the user's media and downloads alone

### The rules the build keeps

- **Never patch upstream.** The one change to an upstream file is the
  backend stage's rewrite of `global.json` by `test/global-json.sh`, which
  keeps upstream's SDK version as the floor and adds `"rollForward":
  "latestFeature"`, so a later SDK band, which Renovate moves the image to,
  can build; it never ships. A different change to Sonarr's source is a
  fork, which the design ruled out
- **Two build-stage checks guard a .NET major**, which would otherwise
  show only as dotnet's own error, or as an app that will not start.
  `test/global-json.sh` holds the SDK `global.json` names to the image's
  major and minor before it rewrites the file; `test/runtime-major.sh`
  holds the runtimeconfigs copied to `/out/bin` to the runtime image's
  tag, which the backend stage reads through a bare `ARG RUNTIME_IMAGE`,
  alone on its line, since Renovate would read anything after it as the
  runtime pin, and declared just above the check, since every later RUN
  has an ARG's value in its cache key and a new runtime pin would
  otherwise rebuild the restore and publish. Each fails a mismatch with
  the steps to take, naming [A .NET major](#a-net-major). Each is a
  build-stage check under "Code quality"'s rules, and its Go test,
  `test/globaljson` or `test/runtimemajor`, also checks the Containerfile
  runs it where it must. The two scripts and their tests are
  oci-image-template's .NET layer's and prowlarr-oci-image's too, byte for
  byte but for the SPDX line, so a change to one goes to all three
- **The version and the branch are always passed.**
  `-p:AssemblyVersion=${SONARR_VERSION}` and
  `-p:AssemblyConfiguration=develop` override upstream's `10.0.0.*` and
  `Release-dev`, under which Sonarr runs as a development build, with
  analytics and Sentry forced on whatever the user sets, and the API
  reporting 10.0.0.x. They are `-p:` properties, so no file is edited. The
  probe compares the status's version with the same value, through
  `-X main.version`. `develop`, here and in `package_info`, is upstream's
  name for v5 builds, until the image moves to stable
- **NuGet: no lock files, signatures verified, advisories warned of.**
  Upstream keeps no `packages.lock.json`, pins every package by an exact
  version from nuget.org alone, whose versions are immutable and
  repository-signed, and NuGet restores the lowest version a range allows.
  Ours would need regenerating by a person at every upstream bump. So the
  backend stage sets `DOTNET_NUGET_SIGNATURE_VERIFICATION=true`, and
  publishes under `--network=none` once restored. Upstream treats every
  warning as an error, NuGet's audit among them, so a new advisory against
  a package an already-pinned commit restores would fail the build, and
  hold back every other update, the runtime's patches included, until
  upstream moved: `-p:WarningsNotAsErrors=NU1901%3BNU1902%3BNU1903%3BNU1904`
  keeps those four warnings, and ci's scan blocks a vulnerable package the
  image does not already ship. The msbuild switch `-warnNotAsError` does
  not reach NuGet; only the property does
- **The frontend runs no install script.** `yarn install
  --frozen-lockfile --ignore-scripts`: Sonarr's own package.json has none,
  the Vite build needs none, and an install script is how most malicious
  npm releases have run. If an upstream bump makes the build need one, the
  build fails, and the fix is to name why in the Containerfile, not to
  drop the flag quietly
- **ffprobe gets its execute bit back.** NuGet extracts it without one, as
  upstream's packaging finds too; the backend stage `chmod`s it and asserts
  it is executable, and `TestFFprobeRuns` runs it
- **Every stage is DHI.** The fetch runs in the Node `-dev` image, which
  ships git, the probe in the Go `-dev` image, and the frontend in the same
  Node image, with yarn 1 and `--frozen-lockfile`. Never add a stage from
  another publisher, and never set `DOTNET_SYSTEM_GLOBALIZATION_INVARIANT`:
  it breaks name matching and release parsing
- **The probe is stdlib Go.** `cmd/divbast-sonarr` and `internal/probe`
  import nothing outside the standard library, since the build context
  leaves `.ci/` out, and divbast/ci's `probekit` would put ci code in the
  image
- **`SONARR_VERSION` and `SONARR_COMMIT` stay adjacent, in that order.**
  `renovate.json`'s first regex manager moves both in one PR, from Sonarr's
  GitHub releases, pre-releases included, never its tags (Sonarr's
  repository holds Radarr's too), and the fetch stage asserts the
  release's tag still names the commit
- **The stable watch stays, until the move to stable.** The
  Containerfile's `# SONARR_STABLE_V5=0.0.0.0` line is `renovate.json`'s
  second manager's: Renovate writes Sonarr's first stable v5 release
  there, offered only releases GitHub calls stable and of v5, and
  `TestNoV5StableYet` (`test/stable`) fails once it moves, and if the line
  is lost or doubled. It lives in the Containerfile because the
  recommended preset ignores every path under `test/`. Its floor, 0.0.0.0,
  shares no major, minor and patch with the `5.0.0.N` develop builds,
  since Renovate offers a pre-release that shares all three with the
  current version. Both managers share the group `Sonarr`, with
  `separateMajorMinor` off so the watch's first, major, update stays in
  the group's branch; a branch holding the watch's update is not
  automerged, and its check holds it red. So once v5 stable exists, the
  develop pins stop moving. Re-prove both managers with a local Renovate
  run before changing either

## A .NET major

A .NET major moves by hand, about once per LTS, because neither of its two
pull requests can go green alone: Sonarr's retargeted release needs the
new SDK, and the SDK and runtime the Dashboard holds back cannot build or
run the release before it. The backend stage's two checks fail each with
a message naming this section; only were Sonarr to drop its `global.json`
and retarget first would dotnet's own error come instead. Each step needs
the user's go-ahead:

1. Sonarr's Renovate PR, the group `Sonarr`, which moves `SONARR_VERSION`
   and `SONARR_COMMIT`, is red with one of the checks' messages, where `W`
   is the new .NET:
   `ERROR: global.json asks for the .NET W SDK; … Approve the .NET W major …`
   or `ERROR: the app targets .NET W; … Approve …`.
2. Approve the major, the "dhi .NET SDK and runtime" group, on the
   Dependency Dashboard. Renovate opens its PR, red on its own: a check
   fails it with `ERROR: … A .NET major waits until … retargets …`, which
   here is expected, and no reason to stop.
3. On a branch from main, take both Renovate commits with
   `git cherry-pick -S`, open a PR, and merge it when green. Nothing the
   Containerfile runs names the target framework, so those two commits are
   the whole change: the backend stage finds the one folder matching
   `_output/net*/<rid>/publish`, and refuses none or several.
4. Close the two Renovate PRs.

## Moving to stable

The image follows Sonarr v5's develop line until upstream publishes v5
stable, and then moves to stable by hand, once. Nobody has to be watching
for it: Renovate notices, and stops the develop line where it is. Each
step needs the user's go-ahead:

1. The group `Sonarr`'s Renovate PR is red: it moves the Containerfile's
   `# SONARR_STABLE_V5=` line to Sonarr's first stable v5 release, and
   `TestNoV5StableYet` fails it with `Sonarr v5 is stable: release <it>.
   The develop line stops here until the image moves to stable: CLAUDE.md,
   'Moving to stable'.` Every develop release after it lands in that same
   PR, so `main` stays on the develop build it has, and stall's `stuck`
   check goes red within a day.
2. Pick the stable release to move to: the first that carries every
   database migration the pinned develop build has, since a Sonarr older
   than the database it opens meets migrations it does not know. Each file
   under `src/NzbDrone.Core/Datastore/Migration/` at `SONARR_COMMIT` must
   be at the stable release's commit, by number and name. `git merge-base
   --is-ancestor <SONARR_COMMIT> <stable commit>` passing settles it at
   once; failing, it does not, since upstream rewrites `develop`, so
   compare the files. With the develop pins stopped before the stable
   release, the first should pass; if it does not, wait for the next.
3. On a branch from main, in one PR:
   - `renovate.json`: the first manager follows stable releases alone, as
     prowlarr-oci-image's does (drop `ignoreUnstable: false` from its rule,
     and say so in its description); delete the stable watch's manager and
     its rule, and the group's `separateMajorMinor` and the description
     that explains them;
   - the Containerfile: `SONARR_VERSION` and `SONARR_COMMIT` to the
     release picked and the commit its tag names; delete the
     `# SONARR_STABLE_V5=` line and its comment;
     `-p:AssemblyConfiguration=main` and `Branch=main`, upstream's names
     for stable builds; the comments that say develop;
   - delete `test/stable`;
   - `test/acceptance`: `developBranch`, and `branchChanged`, to `main`;
   - `build.yml`'s `description`, this file, the README and divbast/.github's
     profile row: drop "beta" and "develop", and this section.

   Open the PR, and merge it when green.
4. Close Renovate's red PR, and mark Sonarr's phase 7 done in the Servarr
   programme's records.

## Where the shared parts live

- **divbast/ci.** `just setup` checks it out into `.ci/`, which git and
  the build context both ignore, at the one commit every workflow calls it
  at: a release's full SHA, with its tag as the comment. Its README is the
  reference for all of it:
  - the reusable workflows: `app-check.yml` (lint; the image built for
    amd64 and arm64 once every image it builds from verifies; the acceptance
    suite on each under Docker and rootless Podman; the scan),
    `app-publish.yml` (the daily slot and the button only: push, index,
    attest, verify, then tag and check as an anonymous client pulls it),
    `renovate.yml` and `stall.yml`. `build.yml`, `renovate.yml` and
    `stall.yml` here call them, and keep their own triggers and the
    `required` job;
  - the recipes the justfile imports, `.ci/just/common.just` and
    `.ci/just/app.just`. Recipes here call them from a recipe's body, never
    as a dependency, so the justfile still parses before `.ci` exists;
  - the Go tools, which `just ci-tools` builds into `.ci/bin/`, run from
    this repository's root: `divbast-pin`, `divbast-verify-images`,
    `divbast-scan`, `divbast-testcount`, `divbast-published-revision`,
    `divbast-publish-decision`, `divbast-stall` and the rest;
  - the acceptance `harness` and the `contract` suite, which `go.mod`
    requires through `replace github.com/divbast/ci => ./.ci`;
  - `go/golangci.yml`, the one golangci-lint configuration, and
    `.github/actionlint.yaml`.

  A change to ci reaches this repository only through the Renovate PR that
  moves every pin of it at once. `just setup`, and the pin check
  (`divbast-pin`) in each ci workflow that runs this repository's code,
  refuse workflows that call ci at more than one commit
- **divbast/renovate-config** holds the presets `renovate.json` extends.
  `renovate.json` adds Sonarr's two managers and their rules, and nothing
  else
- **divbast/infra** holds the org's settings, this repository's rulesets,
  the `divbast-renovate` App, the `renovate` environment and
  `SONARR_AUTOMERGE`

## Key commands

- `just setup` — fetch divbast/ci into `.ci/` at the commit the workflows
  pin, then install the pinned toolchain. One command on a fresh clone, and
  again whenever the pin moves; `just lint` fails, naming it, when `.ci` is
  not at the pinned commit (`ci-fresh`)
- `just build` — build the image locally, after `docker login dhi.io`. It
  does not verify the images it builds from; `just ci-tools &&
  .ci/bin/divbast-verify-images` does, with cosign from `mise.toml`
- `just acceptance` — `go test ./test/acceptance/...` against the image
  `just build` wrote, under `ENGINE` (docker unless it says podman),
  through `divbast-testcount`, which fails the run unless a test passed and
  none failed or skipped. `just IMAGE=… acceptance` tests another image.
  `ENGINE=docker IMAGE=localhost/sonarr:dev mise exec -- go test
  -run TestServes ./test/acceptance/` runs one test. A full run takes
  about two minutes, `TestRefusesNoConfigMount` being the longest
- `just test` — build, then the acceptance suite
- `just lint` — pinact, zizmor, actionlint, ShellCheck and shfmt, the Go
  gates (the compiler, `go vet`, `go fix -diff`, `go mod tidy -diff`,
  `golangci-lint fmt --diff` and `golangci-lint run`), `go-test`, `reuse
  lint`, hadolint and `docker build --check`. `go-test` runs every Go test
  under the race detector, `test/globaljson`, `test/runtimemajor` and
  `test/stable` among them; the acceptance suite skips there, since
  `IMAGE` is unset
- `just archive`, `just scan` — write the image as an OCI archive, then run
  `divbast-scan` over it and the published `:latest`. Needs network access
  to ghcr.io
- `just actions-pin`, `just actions-update` — move the workflows' action pins
- `just ci` — every gate, locally. Run this before committing

## Where the toolchain comes from

- **`mise.toml` is where tool versions are written.** Go, golangci-lint,
  hadolint, ShellCheck, shfmt, actionlint, reuse, just, pinact, trivy,
  zizmor and cosign are all listed there. Every justfile recipe but `setup`, which
  installs them, runs through `mise exec`, so a recipe resolves those
  binaries and not a same-named one on PATH. The recipes that call GitHub,
  pinact's and zizmor's, take a fine-grained token from `GITHUB_TOKEN` and
  nothing else, as divbast/ci's README says; CI passes its own
- **Go builds with the toolchain mise installs and nothing else.**
  `GOTOOLCHAIN=local` keeps `go.mod`'s line from downloading another, and
  `GOFLAGS=-mod=readonly` refuses to edit `go.mod`
- **`just lint` needs a running Docker daemon**, for `docker build --check`.
  `--check` is a buildx flag with no podman equivalent, so this one gate does
  not honour `ENGINE` and does not run without dockerd
- **`just scan` needs `skopeo` on `PATH`.** It reads the published image with
  [skopeo](https://github.com/containers/skopeo), which no `mise.toml` entry
  provides, so `just ci` needs it too. CI runners already have it
- **The acceptance suite under Docker needs `zstd` on `PATH`.** Every
  layer of a DHI image is zstd, and ci's harness reads the layers of the
  image Docker saves through the `zstd` command, since Go's standard
  library has no zstd reader yet; without it the contract's
  `no-file-capability-xattr` check fails, naming the layer. No
  `mise.toml` entry provides it. GitHub's runners have it, and a Mac gets
  it from Homebrew (`brew install zstd`). Podman saves its layers
  uncompressed, so a Podman run does not need it
- **`renovate.json` has a `.license` sidecar.** JSON has no comment syntax,
  and an SPDX tag written as a string value carries the closing quote into
  the expression, which `reuse` rejects

## Code quality

- All linter gates are enforced as errors — fix them, don't suppress them. Any
  suppression, anywhere, needs an explicit, comment-level justification beside
  it to be accepted
- **The scan blocks only what a change would add, and never passes a
  report blind to the OS.** `divbast-scan` scans the candidate and the
  published `:latest` for the same arch from one Trivy database, and fails
  only on a HIGH or CRITICAL finding, the same vulnerability in the same
  package, that the published image lacks. No release can make an
  advisory against something already shipped any worse, so blocking on one
  only holds back whatever else the release fixes. Whether anything is
  published is the registry's to say, never Trivy's: the scan reads
  `:latest` as an anonymous client, and when ghcr.io refuses, asks GitHub
  why, with the check job's token, which is what its `packages: read` is
  for. Not published yet, or a package still private: every finding is
  reported, on the summary page, as a warning annotation and on stderr,
  and none blocks. Once the image is public, only new findings block. A
  public image the registry refuses, a registry that cannot be reached, or
  GitHub not answering fails the scan rather than waving it through. A
  local `just scan` never asks GitHub, so a refusal there is advisory,
  with a warning that only CI can tell what it means. **None of that
  softens the OS guard:** a report in which Trivy recognised no operating
  system, or saw no OS package, fails the scan, for the candidate and the
  published image alike and whether or not anything is published, since
  it would show nothing about the OS packages. The scan applies DHI's
  signed VEX statements for the runtime image, so Trivy's reading of DHI's
  `+dhiN` packages as plain Debian ones does not over-report
- REUSE-compliant SPDX headers on every file; licence is **GPL-3.0-only**.
  The image's licences label names only that. Sonarr's own licence file
  ships in `/app/bin/LICENSE.md`, where upstream's tarball puts it, and the
  label and the README say nothing more of it
- **The acceptance suite is Go, on divbast/ci's harness.** Every
  container that runs Sonarr runs under `harness.Contract("64m")`, the
  *exact* confinement the contract claims, `--security-opt
  no-new-privileges` and `--tmpfs /tmp:size=64m,noexec,nosuid,nodev`
  included, so a check can never pass under looser settings than we ship.
  What CI proves is what ships. The one exception, `asRoot`, runs only
  the suite's own probe (`test/probe`), to leave a volume as root would.
  `TestContract` runs ci's contract suite against the image; the rest are
  Sonarr's own, one topic per file, as the README's "The pipeline" lists
  them. Each serves through the harness's `Serve` (or `StartServing`, for
  a container that must be started apart from its first request), which
  publishes on the host's loopback at ports the engine picks, so no two
  tests, or two suites on one host, ask for the same one. The assertions
  (`StatusIs`, `Answers`, `Refuses` and the rest) are the harness's too,
  shared with every divbast image, and every wait is `harness.Poll`. Each
  thing the image serves or guarantees has a test. Under Podman every
  container gets `--log-driver k8s-file`: Podman's default under systemd,
  journald, drops a unit's messages past its rate limit, and Sonarr's first
  start logs every migration, so a suite run lost the very lines its tests
  wait for
- **Go is modern and idiomatic, and linted strictly**, under ci's
  `go/golangci.yml`. Bash is for what is short and will stay so: the
  justfile's recipes. A check that runs inside a Containerfile build stage
  is the exception, since a build stage has bash and no Go: it stays bash,
  under ShellCheck `enable=all`, shfmt and `set -euo pipefail`, with a Go
  test that runs it against fixtures and sees each way it can refuse
- A test suite that has never failed proves nothing. When adding a check, prove
  it can fail before trusting it
