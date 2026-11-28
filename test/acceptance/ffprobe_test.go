// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// ffprobe is the binary Sonarr runs to read a media file's streams, from
// the folder it runs from. It comes from the ghcr.io/divbast/ffmpeg:servarr
// carrier of divbast/native-tools.
const ffprobe = "/app/bin/ffprobe"

// TestFFprobeRuns runs ffprobe in a serving container, as Sonarr's own
// user under the contract's confinement, as Sonarr runs it on a library
// scan or an import: it must start, which takes its execute bit and a
// binary that needs nothing the base lacks, and say what it is. With no
// series added, Sonarr never runs it on its own, so nothing else would
// show it broken.
func TestFFprobeRuns(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	res, err := s.C.Exec(t.Context(), nil, ffprobe, "-version")
	if err != nil {
		checks.True(ffprobe+" -version runs", false, err.Error())
		return
	}
	out := string(res.Stdout)
	checks.True(ffprobe+" -version exits 0", res.Code == 0,
		fmt.Sprintf("exit status %d, printing %s", res.Code, harness.Clip(out+string(res.Stderr))))
	checks.True(ffprobe+" -version says it is ffprobe", strings.HasPrefix(out, "ffprobe version "), harness.Clip(out))
}

// fixturesDir is the folder of this package's testdata, and where the
// container sees it: ffprobe reads the sample from a file, as Sonarr's
// own analysis does.
const fixturesDir = "/fixtures"

// hdr10Sample is a 128x72, six-frame, 10-bit HEVC elementary stream with
// HDR10 metadata that the image's ffprobe can only report by decoding it.
// It is test patterns that FFmpeg 9.1 generates, so there is no
// third-party media in it, made with:
//
//	ffmpeg -f lavfi -i testsrc2=size=128x72:rate=24:duration=0.25 \
//	  -pix_fmt yuv420p10le -c:v libx265 -preset ultrafast -f hevc \
//	  -x265-params 'colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400:repeat-headers=1:hdr10=1:log-level=error:keyint=3:min-keyint=3' \
//	  hdr10.hevc
//
// The mastering display and light level data are in the bitstream's SEI
// messages alone, so a container cannot supply them.
const hdr10Sample = fixturesDir + "/hdr10.hevc"

// probeOutput is the part of ffprobe's JSON that Sonarr reads: a stream's
// pixel format and colour, and a frame's side data.
type probeOutput struct {
	Streams []struct {
		CodecType      string `json:"codec_type"`
		PixFmt         string `json:"pix_fmt"`
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
	} `json:"streams"`
	Frames []struct {
		SideDataList []struct {
			SideDataType string `json:"side_data_type"`
		} `json:"side_data_list"`
	} `json:"frames"`
}

// TestFFprobeReadsHDR10 runs ffprobe with the command lines Sonarr's
// VideoFileInfoReader gives FFMpegCore, on an HDR10 sample, and holds
// what Sonarr works its bit depth and HDR format out of. TestFFprobeRuns
// cannot tell a working ffprobe from one with no video decoders, which
// starts, prints its version, and never reports a pixel format: Sonarr
// then takes every file for 8-bit, and names HDR files as if they were
// not.
func TestFFprobeReadsHDR10(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	dir := sampleDir(t)
	s := serveSonarr(t, e, image, pingPath, keyed(nil), configVolume(t, e),
		harness.Mount{Source: dir, Target: fixturesDir, ReadOnly: true})
	checks := harness.NewChecks(t)

	// The analysis of every file: its streams, from which Sonarr takes
	// the pixel format, and from that the bit depth, and the colour.
	var streams probeOutput
	if !runProbe(t, s, checks, &streams,
		"-loglevel", "error", "-print_format", "json=compact=1", "-show_format", "-sexagesimal",
		"-show_streams", "-show_chapters", hdr10Sample, "-probesize", "50000000") {
		return
	}
	checks.True("the analysis reports one stream", len(streams.Streams) == 1, strconv.Itoa(len(streams.Streams)))
	if len(streams.Streams) != 1 {
		return
	}
	v := streams.Streams[0]
	checks.True("the stream is video", v.CodecType == "video", v.CodecType)
	checks.True("pix_fmt is a 10-bit format", strings.HasSuffix(v.PixFmt, "10le"), fmt.Sprintf("%q", v.PixFmt))
	checks.True("the colour transfer is PQ", v.ColorTransfer == "smpte2084", fmt.Sprintf("%q", v.ColorTransfer))
	checks.True("the colour primaries are BT.2020", v.ColorPrimaries == "bt2020", fmt.Sprintf("%q", v.ColorPrimaries))

	// Sonarr asks for the first frame of a PQ stream, for the side data
	// that tells HDR10 from PQ10 and HDR10+.
	var frames probeOutput
	if !runProbe(t, s, checks, &frames,
		"-loglevel", "error", "-print_format", "json=compact=1", "-show_frames", "-sexagesimal",
		hdr10Sample, "-read_intervals", "%+#1", "-select_streams", "v:0") {
		return
	}
	checks.True("the frame call returns one frame", len(frames.Frames) == 1, strconv.Itoa(len(frames.Frames)))
	if len(frames.Frames) != 1 {
		return
	}
	var types []string
	for _, d := range frames.Frames[0].SideDataList {
		types = append(types, d.SideDataType)
	}
	checks.True("the frame carries the mastering display data", slices.Contains(types, "Mastering display metadata"), strings.Join(types, ", "))
	checks.True("the frame carries the content light level data", slices.Contains(types, "Content light level metadata"), strings.Join(types, ", "))
}

// runProbe runs ffprobe with args in s's container, and reads its JSON
// into out. It reports whether it could: a failure is recorded in checks.
func runProbe(t *testing.T, s *harness.Serving, checks *harness.Checks, out *probeOutput, args ...string) bool {
	t.Helper()
	res, err := s.C.Exec(t.Context(), nil, append([]string{ffprobe}, args...)...)
	if err != nil {
		checks.True("ffprobe runs", false, err.Error())
		return false
	}
	checks.True("ffprobe exits 0", res.Code == 0,
		fmt.Sprintf("exit status %d, printing %s", res.Code, harness.Clip(string(res.Stdout)+string(res.Stderr))))
	if res.Code != 0 {
		return false
	}
	if err := json.Unmarshal(res.Stdout, out); err != nil {
		checks.True("ffprobe prints JSON", false, err.Error()+": "+harness.Clip(string(res.Stdout)))
		return false
	}
	return true
}

// sampleDir is a fresh host directory holding a copy of the sample, the
// directory at 0755 and the file at 0644, for a bind mount uid 65532 reads
// whatever umask the runner has: a checkout's own modes follow it, and
// rootless Podman maps the mount's owner to another user.
func sampleDir(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("testdata/hdr10.hevc")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := dir + "/hdr10.hevc"
	if err := os.WriteFile(file, body, 0o644); err != nil { //nolint:gosec // G306: read by the container's user, which is not this one
		t.Fatal(err)
	}
	// WriteFile's mode passes through the umask
	if err := os.Chmod(file, 0o644); err != nil { //nolint:gosec // G302: as above
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: as above
		t.Fatal(err)
	}
	return dir
}
