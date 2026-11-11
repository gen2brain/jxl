package jxl

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// CONFORMANCE_DIR is a colon separated list of corpora.
func conformanceDirs(t *testing.T) []string {
	t.Helper()

	env := os.Getenv("CONFORMANCE_DIR")
	if env == "" {
		t.Skip("set CONFORMANCE_DIR")
	}

	return strings.Split(env, ":")
}

func conformanceRoot(t *testing.T) string {
	t.Helper()

	for _, dir := range conformanceDirs(t) {
		if _, err := os.Stat(filepath.Join(dir, "testcases")); err == nil {
			return dir
		}
	}

	t.Skip("no conformance suite in CONFORMANCE_DIR")

	return ""
}

var updateRatchet = flag.Bool("conformance.update", false,
	"rewrite testdata/conformance.txt from this run")

type conformanceSuite struct {
	name     string
	level    int
	baseline int
}

var conformanceSuites = []conformanceSuite{
	{"main_level5", 5, 22},
	{"main_level10", 10, 26},
}

type conformanceCase struct {
	Frames []struct {
		Name string  `json:"name"`
		RMS  float64 `json:"rms_error"`
		Peak float64 `json:"peak_error"`
	} `json:"frames"`
	BitsPerSample    []int    `json:"bits_per_sample"`
	ExpBitsPerSample []int    `json:"exp_bits_per_sample"`
	ExtraChannelType []string `json:"extra_channel_type"`
	IntensityTarget  float64  `json:"intensity_target"`
	OriginalICC      string   `json:"original_icc"`
	ReconstructedJPG string   `json:"reconstructed_jpeg"`
}

func (s conformanceSuite) cases(t *testing.T, root string) []string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(root, "testcases", s.name+".txt"))
	if err != nil {
		t.Skipf("no corpus list for %s", s.name)
	}

	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}

	return names
}

type conformanceResult struct {
	verdict string
	peak    float64
	rms     float64
	note    string
}

func TestConformance(t *testing.T) {
	root := conformanceRoot(t)
	ratchet := readRatchet(t)
	fresh := map[string]conformanceResult{}

	for _, s := range conformanceSuites {
		names := s.cases(t, root)
		counts := map[string]int{}

		for _, name := range names {
			key := s.name + "/" + name
			res := runConformanceCase(t, root, name, s.level, ratchet[key])
			counts[res.verdict]++
			fresh[key] = res

			switch res.verdict {
			case "pass":
			case "drift":
				t.Errorf("%s: %s", key, res.note)
			default:
				t.Logf("%s: %s: %s", key, res.verdict, res.note)
			}
		}

		t.Logf("%s: %d/%d pass, %d drift, %d fail, %d error", s.name,
			counts["pass"], len(names), counts["drift"], counts["fail"], counts["error"])

		if got := counts["pass"]; got < s.baseline {
			t.Errorf("%s: %d pass, baseline is %d", s.name, got, s.baseline)
		}
	}

	if *updateRatchet {
		writeRatchet(t, fresh)
	}
}

func runConformanceCase(t *testing.T, root, name string, level int, prev conformanceResult) conformanceResult {
	t.Helper()

	dir := filepath.Join(root, "testcases", name)

	var desc conformanceCase
	b, err := os.ReadFile(filepath.Join(dir, "test.json"))
	if err != nil {
		return conformanceResult{verdict: "error", note: err.Error()}
	}
	if err := json.Unmarshal(b, &desc); err != nil {
		return conformanceResult{verdict: "error", note: err.Error()}
	}

	ref, err := readNPY(filepath.Join(dir, "reference_image.npy"))
	if err != nil {
		return conformanceResult{verdict: "error", note: err.Error()}
	}

	in, err := os.Open(filepath.Join(dir, "input.jxl"))
	if err != nil {
		return conformanceResult{verdict: "error", note: err.Error()}
	}
	defer in.Close()

	got, err := DecodeFloat(in, Options{Level: level})
	if err != nil {
		return conformanceResult{verdict: "error", note: err.Error()}
	}

	if len(got.Frames) != ref.frames || got.Height != ref.height ||
		got.Width != ref.width || got.Channels != ref.channels {
		return conformanceResult{verdict: "fail", note: fmt.Sprintf(
			"shape %dx%dx%dx%d, want %dx%dx%dx%d",
			len(got.Frames), got.Height, got.Width, got.Channels,
			ref.frames, ref.height, ref.width, ref.channels)}
	}
	if len(desc.Frames) != ref.frames {
		return conformanceResult{verdict: "error", note: fmt.Sprintf(
			"test.json has %d frames, reference has %d", len(desc.Frames), ref.frames)}
	}

	res := conformanceResult{verdict: "pass"}

	for i, limit := range desc.Frames {
		peak, rms := compareFrame(got.Frames[i], ref.frame(i), ref.channels)
		res.peak = math.Max(res.peak, peak)
		res.rms = math.Max(res.rms, rms)

		if res.verdict == "pass" {
			if rms > limit.RMS {
				res.verdict = "fail"
				res.note = fmt.Sprintf("frame %d: rms %g > %g", i, rms, limit.RMS)
			} else if peak > limit.Peak {
				res.verdict = "fail"
				res.note = fmt.Sprintf("frame %d: peak %g > %g", i, peak, limit.Peak)
			}
		}
	}

	if res.verdict != "pass" {
		return res
	}

	if prev.verdict == "pass" {
		if grew, was, now := ratchetGrew(prev, res); grew {
			res.verdict = "drift"
			res.note = fmt.Sprintf("error grew: %g, recorded %g", now, was)
		}
	}

	return res
}

func ratchetGrew(prev, res conformanceResult) (bool, float64, float64) {
	const band, floor = 1.25, 1e-7

	if res.peak > prev.peak*band+floor {
		return true, prev.peak, res.peak
	}
	if res.rms > prev.rms*band+floor {
		return true, prev.rms, res.rms
	}

	return false, 0, 0
}

func compareFrame(got, want []float32, channels int) (peak, rms float64) {
	sum := make([]float64, channels)
	n := len(want) / channels

	for i, w := range want {
		d := math.Abs(float64(got[i]) - float64(w))
		if d > peak {
			peak = d
		}
		sum[i%channels] += d * d
	}

	for _, s := range sum {
		if r := math.Sqrt(s / float64(n)); r > rms {
			rms = r
		}
	}

	return peak, rms
}

const ratchetPath = "testdata/conformance.txt"

func readRatchet(t *testing.T) map[string]conformanceResult {
	t.Helper()

	m := map[string]conformanceResult{}

	f, err := os.Open(ratchetPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		return m
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if i := strings.Index(text, "#"); i >= 0 {
			text = text[:i]
		}

		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			t.Fatalf("%s:%d: want 'case peak rms'", ratchetPath, line)
		}

		peak, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("%s:%d: %v", ratchetPath, line, err)
		}

		rms, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			t.Fatalf("%s:%d: %v", ratchetPath, line, err)
		}

		m[fields[0]] = conformanceResult{verdict: "pass", peak: peak, rms: rms}
	}

	return m
}

func writeRatchet(t *testing.T, res map[string]conformanceResult) {
	t.Helper()

	keys := make([]string, 0, len(res))
	for k, v := range res {
		if v.verdict == "pass" || v.verdict == "drift" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# case peak rms, from go test -run TestConformance -conformance.update\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %g %g\n", k, res[k].peak, res[k].rms)
	}

	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ratchetPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Logf("wrote %s with %d cases", ratchetPath, len(keys))
}

func TestConformanceICC(t *testing.T) {
	root := conformanceRoot(t)

	paths, err := filepath.Glob(filepath.Join(root, "testcases", "*", "original.icc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no original.icc in the corpus")
	}

	for _, path := range paths {
		dir := filepath.Dir(path)

		t.Run(filepath.Base(dir), func(t *testing.T) {
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(filepath.Join(dir, "input.jxl"))
			if err != nil {
				t.Fatal(err)
			}

			d, err := newDecoder(bytes.NewReader(data), Options{Level: 10})
			if err != nil {
				t.Fatal(err)
			}

			if !d.hdr.meta.color.wantICC {
				t.Skip("enumerated colour encoding: the ICC is generated, not decoded")
			}

			if len(d.iccp) != len(want) {
				t.Fatalf("decoded %d bytes, want %d", len(d.iccp), len(want))
			}
			for i := range want {
				if d.iccp[i] != want[i] {
					t.Fatalf("byte %d is %#02x, want %#02x", i, d.iccp[i], want[i])
				}
			}

			t.Logf("%d bytes, byte-exact", len(want))
		})
	}
}

// apngFrames rebuilds every frame of an APNG as a standalone PNG, so libjxl's
// own decode of an animation can be read back with the standard library.
func apngFrames(t *testing.T, path string) []image.Image {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var (
		header []byte
		before [][]byte
		frames []image.Image
		parts  [][]byte
		rect   image.Rectangle
	)

	flush := func() {
		if len(parts) == 0 {
			return
		}

		var buf bytes.Buffer

		buf.Write(data[:8])

		ihdr := slices.Clone(header)
		binary.BigEndian.PutUint32(ihdr[0:], uint32(rect.Dx()))
		binary.BigEndian.PutUint32(ihdr[4:], uint32(rect.Dy()))

		writeChunk(&buf, "IHDR", ihdr)

		for _, c := range before {
			writeChunk(&buf, string(c[:4]), c[4:])
		}

		for _, p := range parts {
			writeChunk(&buf, "IDAT", p)
		}

		writeChunk(&buf, "IEND", nil)

		img, err := png.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("%s: frame %d: %v", path, len(frames), err)
		}

		frames = append(frames, img)
		parts = nil
	}

	for i := 8; i+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		body := data[i+8 : i+8+n]

		switch typ {
		case "IHDR":
			header = slices.Clone(body)
		case "PLTE", "tRNS":
			before = append(before, append([]byte(typ), body...))
		case "fcTL":
			flush()

			rect = image.Rect(0, 0, int(binary.BigEndian.Uint32(body[4:])),
				int(binary.BigEndian.Uint32(body[8:])))
		case "IDAT":
			parts = append(parts, slices.Clone(body))
		case "fdAT":
			parts = append(parts, slices.Clone(body[4:]))
		}

		i += 12 + n
	}

	flush()

	return frames
}

func writeChunk(buf *bytes.Buffer, typ string, body []byte) {
	var head [4]byte

	binary.BigEndian.PutUint32(head[:], uint32(len(body)))
	buf.Write(head[:])

	crc := crc32.NewIEEE()

	buf.WriteString(typ)
	crc.Write([]byte(typ))

	buf.Write(body)
	crc.Write(body)

	binary.BigEndian.PutUint32(head[:], crc.Sum32())
	buf.Write(head[:])
}

// TestAnimationCorpus checks every frame of every animated corpus file against
// libjxl, which a first-frame comparison cannot do.
func TestAnimationCorpus(t *testing.T) {
	root := corpusRoot(t)

	djxl, err := exec.LookPath("djxl")
	if err != nil {
		t.Skip("djxl is not on the path")
	}

	dir := t.TempDir()
	checked, total := 0, 0

	for _, sub := range corpusSubdirs {
		files, err := filepath.Glob(filepath.Join(root, sub, "*.jxl"))
		if err != nil {
			t.Fatal(err)
		}

		for _, path := range files {
			name := sub + "/" + filepath.Base(path)

			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			ours, err := DecodeAll(f, Options{Level: 10})
			f.Close()

			if err != nil || len(ours.Image) < 2 {
				continue
			}

			out := filepath.Join(dir, "frames.apng")

			if o, err := exec.Command(djxl, path, out).CombinedOutput(); err != nil {
				t.Errorf("%s: djxl gave %v\n%s", name, err, o)

				continue
			}

			theirs := apngFrames(t, out)

			if len(theirs) != len(ours.Image) {
				t.Errorf("%s: %d frames, libjxl gives %d", name, len(ours.Image), len(theirs))

				continue
			}

			checked++

			for i := range theirs {
				worst := 0.0
				b := theirs[i].Bounds()

				for y := range b.Dy() {
					for x := range b.Dx() {
						r1, g1, b1, a1 := ours.Image[i].At(x, y).RGBA()
						r2, g2, b2, a2 := theirs[i].At(b.Min.X+x, b.Min.Y+y).RGBA()

						for _, d := range [4]float64{
							float64(r1) - float64(r2), float64(g1) - float64(g2),
							float64(b1) - float64(b2), float64(a1) - float64(a2),
						} {
							worst = math.Max(worst, math.Abs(d)/65535)
						}
					}
				}

				total++

				if worst > corpusTolerance {
					t.Errorf("%s: frame %d differs from libjxl by %g", name, i, worst)
				}
			}
		}
	}

	if checked == 0 {
		t.Skip("no animations in the corpus")
	}

	t.Logf("%d animations, %d frames within %g of libjxl", checked, total, corpusTolerance)
}

func corpusRoot(t *testing.T) string {
	t.Helper()

	for _, dir := range conformanceDirs(t) {
		if _, err := os.Stat(filepath.Join(dir, "reference")); err == nil {
			return dir
		}
	}

	t.Skip("no image corpus in CONFORMANCE_DIR")

	return ""
}

// Refused files, with the reason. The set only shrinks.
var corpusUnsupported = map[string]string{
	"features/ec_resampling_2x.jxl": "extra channel upsampled independently of the colour channels",
	"features/ec_resampling_4x.jxl": "extra channel upsampled independently of the colour channels",
	"photographic/candle.jxl":       "float16 samples",
}

// Largest difference from libjxl this corpus reaches, of full scale.
const corpusTolerance = 0.01

var corpusSubdirs = []string{"conformance", "features", "photographic", "edge-cases"}

// TestCorpus compares an external corpus against libjxl's own decodes.
func associatedAlpha(data []byte) bool {
	d, err := newBytesDecoder(data, Options{Level: 10})
	if err != nil {
		return false
	}

	a := d.hdr.meta.alphaIndex()

	return a >= 0 && d.hdr.meta.extraChannels[a].alphaAssociated
}

func TestCorpus(t *testing.T) {
	root := corpusRoot(t)

	var exact, near, refused, total int

	for _, sub := range corpusSubdirs {
		files, err := filepath.Glob(filepath.Join(root, sub, "*.jxl"))
		if err != nil {
			t.Fatal(err)
		}

		for _, path := range files {
			name := sub + "/" + filepath.Base(path)
			total++

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			got, err := Decode(bytes.NewReader(data), Options{Level: 10})
			if err != nil {
				why, known := corpusUnsupported[name]
				if !known {
					t.Errorf("%s: %v", name, err)

					continue
				}

				refused++

				t.Logf("%s: refused, %s", name, why)

				continue
			}

			if cfg, err := decodeConfig(data); err != nil || cfg.ColorModel != got.ColorModel() ||
				cfg.Width != got.Bounds().Dx() || cfg.Height != got.Bounds().Dy() {
				t.Errorf("%s: config %+v, %v; image %T %v", name, cfg, err, got, got.Bounds())
			}

			ref := filepath.Join(root, "reference", sub,
				strings.TrimSuffix(filepath.Base(path), ".jxl")+".png")

			worst, err := peakAgainstPNG(got, ref, associatedAlpha(data))
			if err != nil {
				t.Logf("%s: no reference (%v)", name, err)

				continue
			}

			switch {
			case worst == 0:
				exact++
			case worst <= corpusTolerance:
				near++
			default:
				t.Errorf("%s: differs from libjxl by %g", name, worst)
			}
		}
	}

	if total == 0 {
		t.Skip("no corpus files present")
	}

	t.Logf("%d files: %d exact, %d within %g, %d knowingly refused",
		total, exact, near, corpusTolerance, refused)

	if refused != len(corpusUnsupported) {
		t.Errorf("%d files refused, the known set has %d", refused, len(corpusUnsupported))
	}
}

func imageDifference(src, got image.Image) int {
	b, gb := src.Bounds(), got.Bounds()

	worst := 0

	for y := range b.Dy() {
		for x := range b.Dx() {
			r1, g1, b1, a1 := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r2, g2, b2, a2 := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()

			for _, d := range [4]int{
				int(r1) - int(r2), int(g1) - int(g2), int(b1) - int(b2), int(a1) - int(a2),
			} {
				worst = max(worst, max(d, -d))
			}
		}
	}

	return worst
}

func alphaError(src, got image.Image) int {
	b, gb := src.Bounds(), got.Bounds()

	worst := 0

	for y := range b.Dy() {
		for x := range b.Dx() {
			_, _, _, a1 := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			_, _, _, a2 := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()

			worst = max(worst, int(math.Abs(float64(a1)-float64(a2))))
		}
	}

	return worst
}

func opaqueError(src, got image.Image) (float64, float64) {
	b, gb := src.Bounds(), got.Bounds()

	var sum, peak float64

	n := 0

	for y := range b.Dy() {
		for x := range b.Dx() {
			r1, g1, b1, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if a != 0xffff {
				continue
			}

			r2, g2, b2, _ := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			n++

			for _, d := range [3]float64{
				float64(r1) - float64(r2),
				float64(g1) - float64(g2),
				float64(b1) - float64(b2),
			} {
				d /= 65535
				sum += d * d
				peak = math.Max(peak, math.Abs(d))
			}
		}
	}

	if n == 0 {
		return 0, 0
	}

	return math.Sqrt(sum / float64(3*n)), peak
}

// Largest root mean square error a corpus image reaches, of full scale. The
// two 3x3 files set it, and cjxl is no better on them.
const encodeCorpusTolerance = 0.12

// TestEncodeCorpus encodes every corpus reference and reads it back, with djxl
// as the second opinion when it is on the path.
func TestEncodeCorpus(t *testing.T) {
	root := corpusRoot(t)
	djxl, _ := exec.LookPath("djxl")
	dir := t.TempDir()

	type result struct {
		name       string
		rmse, peak float64
	}

	var worst []result

	total, size, lossless := 0, 0, 0

	for _, sub := range corpusSubdirs {
		files, err := filepath.Glob(filepath.Join(root, "reference", sub, "*.png"))
		if err != nil {
			t.Fatal(err)
		}

		for _, path := range files {
			name := sub + "/" + filepath.Base(path)

			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			src, err := png.Decode(f)
			f.Close()

			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			var buf bytes.Buffer

			if err := Encode(&buf, src); err != nil {
				t.Errorf("%s: encode gave %v", name, err)

				continue
			}

			data := buf.Bytes()
			total++
			size += len(data)

			got, err := Decode(bytes.NewReader(data), Options{Level: 10})
			if err != nil {
				t.Errorf("%s: our decoder gave %v", name, err)

				continue
			}

			if got.Bounds().Dx() != src.Bounds().Dx() || got.Bounds().Dy() != src.Bounds().Dy() {
				t.Errorf("%s: decoded %v, source is %v", name, got.Bounds(), src.Bounds())

				continue
			}

			rmse, peak := opaqueError(src, got)
			worst = append(worst, result{name, rmse, peak})

			if d := alphaError(src, got); d != 0 {
				t.Errorf("%s: alpha is %d off, it is coded losslessly", name, d)
			}

			var raw bytes.Buffer

			if err := Encode(&raw, src, EncodeOptions{Lossless: true, Effort: 1}); err != nil {
				t.Errorf("%s: lossless encode gave %v", name, err)

				continue
			}

			exact, err := Decode(bytes.NewReader(raw.Bytes()), Options{Level: 10})
			if err != nil {
				t.Errorf("%s: lossless decode gave %v", name, err)

				continue
			}

			if d := imageDifference(src, exact); d != 0 {
				t.Errorf("%s: lossless is %d off", name, d)
			}

			lossless += raw.Len()

			if rmse > encodeCorpusTolerance {
				t.Errorf("%s: root mean square error %g", name, rmse)
			}

			if djxl == "" {
				continue
			}

			out := filepath.Join(dir, "ours.jxl")

			if err := os.WriteFile(out, data, 0o600); err != nil {
				t.Fatal(err)
			}

			if o, err := exec.Command(djxl, out, filepath.Join(dir, "ours.png")).CombinedOutput(); err != nil {
				t.Errorf("%s: djxl refused our file: %v\n%s", name, err, o)
			}
		}
	}

	if total == 0 {
		t.Skip("no corpus references present")
	}

	slices.SortFunc(worst, func(a, b result) int { return cmp.Compare(b.rmse, a.rmse) })

	for _, r := range worst[:min(5, len(worst))] {
		t.Logf("%-42s rmse %.4f peak %.4f", r.name, r.rmse, r.peak)
	}

	t.Logf("%d images encoded to %d bytes, %d bytes lossless, djxl read them all: %v",
		total, size, lossless, djxl != "")
}

// TestExifCorpus reads the metadata of every corpus file that carries any,
// against libjxl's own writers rather than boxes this package built.
func TestExifCorpus(t *testing.T) {
	root := corpusRoot(t)

	var withExif, withXMP, oriented, compressed, total int

	for _, sub := range corpusSubdirs {
		files, err := filepath.Glob(filepath.Join(root, sub, "*.jxl"))
		if err != nil {
			t.Fatal(err)
		}

		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			name := sub + "/" + filepath.Base(path)
			total++

			e, err := DecodeExif(bytes.NewReader(data))

			switch {
			case err == nil:
				withExif++

				if e.Orientation < 1 || e.Orientation > 8 {
					t.Errorf("%s: orientation %d", name, e.Orientation)
				}

				if e.Orientation != 1 {
					oriented++
				}
			case errors.Is(err, ErrUnsupported):
				compressed++
			case !errors.Is(err, ErrNoExif):
				t.Errorf("%s: %v", name, err)
			}

			switch _, err := RawXMP(bytes.NewReader(data)); {
			case err == nil:
				withXMP++
			case errors.Is(err, ErrUnsupported):
			case !errors.Is(err, ErrNoXMP):
				t.Errorf("%s: xmp: %v", name, err)
			}
		}
	}

	if total == 0 {
		t.Skip("no corpus files present")
	}

	t.Logf("%d files: %d with exif (%d oriented), %d with xmp, %d brotli compressed",
		total, withExif, oriented, withXMP, compressed)
}
