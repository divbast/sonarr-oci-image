# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

# Renovate moves these two in one PR; its regex needs them adjacent and in
# this order, and follows Sonarr's v5 develop releases, each of which
# upstream publishes as a GitHub pre-release, until v5 stable exists. The
# version is all four numbers: the image is tagged with it (app-publish.yml
# reads it from this line, by the name build.yml gives as version-arg), the
# assemblies carry it, and the probe compares it with the version Sonarr
# reports. The fetch stage checks out the commit and asserts the release's
# tag still names it.
ARG SONARR_VERSION=5.0.0.3041
ARG SONARR_COMMIT=03f822c78293981e6e1459fbce59d11476b2107f
# The watch for Sonarr's first stable v5 release, which no build reads:
# Renovate writes it on the line below once GitHub has a v5 release that is
# not a pre-release, and test/stable's TestNoV5StableYet then fails, so the
# develop line stops here until the image moves to stable: CLAUDE.md,
# 'Moving to stable'. 0.0.0.0 stands for none.
# SONARR_STABLE_V5=0.0.0.0
# The runtime: Docker Hardened Images' ASP.NET Core on Debian 13, with ICU,
# OpenSSL and the .NET runtime the app runs on, no shell and no package
# manager, and its nonroot user 65532. The app is built framework-dependent,
# so a rebuild of this image patches the runtime. CI verifies its signature
# by Docker before anything is built, as it does every stage's image below.
ARG RUNTIME_IMAGE=dhi.io/aspnetcore:10.0.15-debian13@sha256:ae9e6b90e72d386ed60d562f169305f5323c6e079dee72bd30980b086069bc8c

# The same image as the frontend's, which ships git and CA certificates.
FROM dhi.io/node:26.13.1-debian13-dev@sha256:a54772ee91034c3340109381226e45b7fb0ede548518ed7b8bfa0d5e5b64b2a0 AS fetch
ARG SONARR_VERSION
ARG SONARR_COMMIT
# git checks a fetch by commit against its hash. The tag is asked for
# separately, peeled if it is ever annotated, so a tag moved upstream fails
# the build instead of shipping a commit no release names. Upstream
# rewrites its develop branch at times, so a release's commit can leave the
# branch; its tag keeps it fetchable, and a fetch by commit never depends
# on the branch. The fetch and the tag lookup each get three retries, 10s,
# 30s and 90s apart, so one dropped connection does not cost the daily
# slot. The remote is named so that the .NET build's Source Link finds the
# repository it came from.
RUN set -eux; \
    retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    git init -q /src; \
    git -C /src remote add origin https://github.com/Sonarr/Sonarr.git; \
    retry git -C /src fetch -q --depth 1 origin "${SONARR_COMMIT}"; \
    git -C /src checkout -q --detach FETCH_HEAD; \
    head=$(git -C /src rev-parse HEAD); \
    test "${head}" = "${SONARR_COMMIT}"; \
    retry git -C /src ls-remote origin \
        "refs/tags/v${SONARR_VERSION}" "refs/tags/v${SONARR_VERSION}^{}" >/tmp/tag; \
    tagged=$(awk -v tag="refs/tags/v${SONARR_VERSION}" \
        '$2 == tag "^{}" { peeled = $1 } $2 == tag { plain = $1 } END { print (peeled != "" ? peeled : plain) }' /tmp/tag); \
    if [ "${tagged}" != "${SONARR_COMMIT}" ]; then \
        echo "tag v${SONARR_VERSION} names '${tagged}', the Containerfile pins ${SONARR_COMMIT}" >&2; \
        exit 1; \
    fi

# The UI, as upstream's CI builds it, with Vite, into _output/UI. yarn
# 1.22.22 ships in the image, and yarn.lock pins every package by its
# sha512. No dependency's install script runs: Sonarr's own package.json
# has none, the build needs none, and an install script is how most
# malicious npm releases have run, so a compromised package that reaches
# yarn.lock through upstream has one way fewer to act. NODE_ENV stays unset
# for the install, where production would skip the devDependencies Vite is
# among; vite build sets it for the build.
FROM dhi.io/node:26.13.1-debian13-dev@sha256:a54772ee91034c3340109381226e45b7fb0ede548518ed7b8bfa0d5e5b64b2a0 AS frontend
WORKDIR /src
COPY --from=fetch /src ./
RUN yarn install --frozen-lockfile --ignore-scripts --network-timeout 120000 \
    && yarn build

# The backend, framework-dependent for this stage's own architecture.
FROM dhi.io/dotnet:10.0.404-sdk-debian13@sha256:f1b04e72766e8a3b8a1061520b0c64f91ad52ef45979c16df90384523fd68783 AS backend
ARG SONARR_VERSION
ARG TARGETARCH
# Upstream keeps no lock file, and pins every package by an exact version
# from nuget.org alone, which signs each one: NuGet checks those signatures
# here.
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NUGET_SIGNATURE_VERIFICATION=true
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
WORKDIR /src
COPY --from=fetch /src ./
COPY test/global-json.sh /tmp/global-json.sh
# The one change made to an upstream file, which never leaves this stage.
# global.json pins the SDK with no roll-forward policy, which accepts only
# that feature band, so an SDK of a later band, which Renovate moves this
# stage's image to, would refuse to build. The script keeps upstream's
# version as the floor and accepts any later band of the same major and
# minor, after checking that major and minor are this image's: a .NET
# major on one side alone fails here, with the steps to take. dotnet asked
# from / reports the image's own SDK, since no global.json is in the way
# there; asked from the source, it would refuse before the rewrite.
# hadolint's DL3003 asks for WORKDIR in place of that cd, which lasts for
# the one command substitution: WORKDIR would move the whole RUN away from
# the source.
# hadolint ignore=DL3003
RUN sdk=$(cd / && dotnet --version); /tmp/global-json.sh global.json "${sdk}"
# The properties restore and publish share, one per line, in a response
# file both read. The version is passed rather than left at upstream's
# 10.0.0.*, which runs as a development build, with analytics and Sentry
# forced on whatever the user sets; -p: overrides it, and the branch,
# without editing Directory.Build.props. The branch is develop, upstream's
# own name for v5 builds, until the image moves to stable: CLAUDE.md,
# 'Moving to stable'. Upstream treats every warning as an error, NuGet's
# audit of known advisories among them, so a new advisory against any
# package an already-pinned commit restores would fail the build, and hold
# back every update, the runtime's patches included, until upstream moves.
# Those four stay warnings here, printed in the build log: ci's scan
# already blocks a vulnerable package the image does not already ship. %3B
# is msbuild's escaped semicolon, which keeps the four in one property.
RUN case "${TARGETARCH}" in \
        amd64) rid=linux-x64 ;; \
        arm64) rid=linux-arm64 ;; \
        *) echo "no .NET runtime identifier for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    printf '%s\n' -p:Configuration=Release -p:Platform=Posix -p:SelfContained=false \
        "-p:RuntimeIdentifiers=${rid}" "-p:AssemblyVersion=${SONARR_VERSION}" \
        -p:AssemblyConfiguration=develop -p:EnableAnalyzers=false -p:SentryUploadSymbols=false \
        -p:WarningsNotAsErrors=NU1901%3BNU1902%3BNU1903%3BNU1904 \
        >/tmp/build.rsp; \
    printf '%s\n' "${rid}" >/tmp/rid
RUN dotnet msbuild src/Sonarr.sln -t:Restore @/tmp/build.rsp
# Everything it needs was restored above, so the publish runs offline.
RUN --network=none dotnet msbuild src/Sonarr.sln -t:PublishAllRids @/tmp/build.rsp
# /app/bin as upstream's Linux package lays it out, without the Windows and
# service helpers, and without Sonarr.Update, the in-app updater the image
# never runs, which publish writes to a folder of its own: package_info
# names Docker as the update mechanism, and upstream reads it only from
# beside a directory named bin. Publish writes the app under its target
# framework, _output/net10.0 today, which a .NET major moves, so the folder
# is found rather than named, and must be the only one: none means publish
# wrote it somewhere else, and with several nothing says which is the app.
# nullglob, for the find alone, makes no match an empty list rather than
# the pattern itself. NuGet extracts ffprobe, which Sonarr runs to read
# media files, without its execute bit, as upstream's packaging finds too;
# it is a static binary, so it needs nothing else from the base. Branch is
# develop until the image moves to stable: CLAUDE.md, 'Moving to stable'.
RUN rid=$(</tmp/rid); \
    shopt -s nullglob; \
    published=(_output/net*/"${rid}"/publish); \
    shopt -u nullglob; \
    if ((${#published[@]} != 1)); then \
        echo "ERROR: ${#published[@]} folders match _output/net*/${rid}/publish, where publish writes the app to one: ${published[*]:-none}." >&2; \
        exit 1; \
    fi; \
    mkdir -p /out/config; \
    cp -a "${published[0]}" /out/bin; \
    rm /out/bin/Sonarr.Windows.* /out/bin/ServiceInstall* /out/bin/ServiceUninstall*; \
    test ! -e /out/bin/Sonarr.Update; \
    chmod 0755 /out/bin/ffprobe; \
    test -x /out/bin/ffprobe; \
    printf '%s\n' "PackageVersion=${SONARR_VERSION}" \
        'PackageAuthor=[divbast](https://github.com/divbast/sonarr-oci-image)' \
        UpdateMethod=Docker Branch=develop >/out/package_info
COPY test/runtime-major.sh /tmp/runtime-major.sh
# With no default of its own, RUNTIME_IMAGE takes the global one at the
# top of this file, which the runtime stage is built FROM. It is declared
# here, for the check alone: BuildKit gives every ARG in scope to each
# later RUN as its environment, and so to its cache key, and declared any
# higher it would have a new runtime pin rebuild the restore and publish.
# It stands alone, with nothing after it on its line: Renovate reads ARG
# NAME VALUE as NAME=VALUE, so anything there would become the runtime
# pin it moves.
ARG RUNTIME_IMAGE
# An app built for one .NET does not start on another's runtime, and
# nothing else would say so: the script compares the .NET the shipped
# runtimeconfigs name with the one leading the runtime image's tag, and
# fails a mismatch with the steps to take. It reads /out/bin once nothing
# more is written there, so what it passes is what ships.
RUN /tmp/runtime-major.sh /out/bin "${RUNTIME_IMAGE}"

# The image's health probe, static and on the standard library alone.
FROM dhi.io/golang:1.27.4-debian13-dev@sha256:730e4ca865af1b931d6b1ea8be41e38b4664b6195223e84f89244cda436201a8 AS probe
ARG SONARR_VERSION
WORKDIR /src
# go.mod's replace names ./.ci, which the build context leaves out: the
# probe imports nothing from divbast/ci, so Go never looks for it
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local \
    go build -trimpath -ldflags "-s -w -X main.version=${SONARR_VERSION}" \
    -o /out/divbast-sonarr ./cmd/divbast-sonarr

FROM ${RUNTIME_IMAGE}
ARG SONARR_VERSION

# Root-owned, and never written: Sonarr writes to /config and TMPDIR alone
COPY --from=backend /out/bin /app/bin
COPY --from=frontend /src/_output/UI /app/bin/UI
COPY --from=backend /out/package_info /app/package_info
# Sonarr's licence, where upstream's release tarball puts it: its packaging
# copies it into the tarball's Sonarr/ folder, which is this /app/bin
COPY --from=fetch /src/LICENSE.md /app/bin/LICENSE.md
COPY --from=probe /out/divbast-sonarr /usr/local/bin/divbast-sonarr
# The data directory, empty and uid 65532's, and not a VOLUME: a new named
# volume mounted here takes its owner from it, so Sonarr can write to it.
# Without a mount, the read-only root refuses every write, and Sonarr
# exits 3, naming /config, though only after about two minutes.
COPY --from=backend --chown=65532:65532 /out/config /config
# Sonarr takes ASP.NET Core's content root from its working directory, and
# sets none of its own. It loads no appsettings.json and watches no file,
# so / would do as well, but the content root is then a folder Sonarr's own
# files are in, never the whole filesystem.
WORKDIR /app/bin

# Any config.xml key can be set as SONARR__<SECTION>__<KEY>. The port is
# 8080, as every divbast image's. The runtime opens no diagnostics socket.
# PGGSSENCMODE stops Npgsql probing for GSS encryption, which matters only
# for PostgreSQL. Analytics and authentication stay as upstream ships them:
# analytics on, and the Forms login, whose first password Sonarr logs
# unless SONARR__AUTH__INITIALPASSWORD sets it.
ENV SONARR__SERVER__PORT=8080 \
    DOTNET_EnableDiagnostics=0 \
    PGGSSENCMODE=disable \
    TMPDIR=/tmp

LABEL org.opencontainers.image.source="https://github.com/divbast/sonarr-oci-image" \
    org.opencontainers.image.url="https://github.com/divbast/sonarr-oci-image" \
    org.opencontainers.image.title="Sonarr" \
    org.opencontainers.image.version="${SONARR_VERSION}" \
    org.opencontainers.image.licenses="GPL-3.0-only"

EXPOSE 8080
# Exec form: the shell form would need /bin/sh, which the image lacks.
# --timeout must clear divbast-sonarr health's own budget: it allows each
# of its two exchanges 4 s and runs them one after the other, so a listener
# that accepts and never answers costs 8 s. A shorter timeout kills the
# probe mid-flight and records a bare engine timeout instead of the probe's
# own line, which is the only diagnostic there is. Podman reads no
# HEALTHCHECK from an image published as an OCI index: its users pass the
# same array as --health-cmd. Sonarr listens only once its database
# migrations finish, and a v4 database meeting v5 runs all of v5's:
# migrations that outlast the start period and three failed checks get the
# container marked unhealthy, and autoheal or a Swarm service would then
# kill it mid-migration. The first check that passes ends the start period,
# so a long one costs a healthy start nothing; it only delays the verdict
# on a start that never comes up. Hence 300 s. The contract suite's
# Healthcheck says the same, and must.
HEALTHCHECK --interval=30s --timeout=10s --start-period=300s --retries=3 \
    CMD ["/usr/local/bin/divbast-sonarr", "health"]
USER 65532:65532
# Sonarr itself runs as PID 1, with nothing before it. .NET handles SIGTERM
# there and reaps its child processes, so it needs no init; a restart from
# the UI happens inside the process. The apphost finds the runtime through
# the base's DOTNET_ROOT. -exitimmediately is upstream's own flag, read in
# one place: a startup failure upstream calls non-recoverable, such as an
# unwritable data directory, then exits 3 at once, where without it Sonarr
# would wait an hour for a key that no one can press in a container, and
# the container would sit there looking alive.
ENTRYPOINT ["/app/bin/Sonarr", "-nobrowser", "-exitimmediately", "-data=/config"]
