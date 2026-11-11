package jxl

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type pfm struct {
	width    int
	height   int
	channels int
	data     []float32
}

func readPFM(path string) (*pfm, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var src io.Reader = f

	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer zr.Close()

		src = zr
	}

	br := bufio.NewReader(src)

	var header [3]string
	for i := range header {
		if header[i], err = br.ReadString('\n'); err != nil {
			return nil, err
		}
	}

	channels := 3
	switch header[0] {
	case "PF\n":
	case "Pf\n":
		channels = 1
	default:
		return nil, fmt.Errorf("not a PFM: %q", header[0])
	}

	var w, h int
	if _, err := fmt.Sscan(header[1], &w, &h); err != nil {
		return nil, err
	}

	scale, err := strconv.ParseFloat(header[2][:len(header[2])-1], 64)
	if err != nil {
		return nil, err
	}

	order := binary.ByteOrder(binary.LittleEndian)
	if scale > 0 {
		order = binary.BigEndian
	}

	raw := make([]byte, w*h*channels*4)
	if _, err := io.ReadFull(br, raw); err != nil {
		return nil, err
	}

	data := make([]float32, w*h*channels)

	for y := range h {
		src := (h - 1 - y) * w * channels * 4

		for i := range w * channels {
			data[y*w*channels+i] = math.Float32frombits(order.Uint32(raw[src+i*4:]))
		}
	}

	return &pfm{w, h, channels, data}, nil
}

func referencePath(path string) string {
	return strings.TrimSuffix(path, ".jxl") + ".pfm.gz"
}

func decodeStream(t *testing.T, path string) *Float {
	t.Helper()

	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	got, err := DecodeFloat(in, Options{Level: 10})
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			t.Skipf("%v", err)
		}

		t.Fatal(err)
	}

	return got
}

func peakAgainst(t *testing.T, got *Float, path string) float64 {
	t.Helper()

	ref, err := readPFM(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.Width != ref.width || got.Height != ref.height {
		t.Fatalf("%dx%d, the reference is %dx%d", got.Width, got.Height, ref.width, ref.height)
	}

	var peak float64

	for y := range ref.height {
		for x := range ref.width {
			for c := range ref.channels {
				g := got.Frames[0][(y*got.Width+x)*got.Channels+min(c, got.Channels-1)]
				w := ref.data[(y*ref.width+x)*ref.channels+c]

				if d := math.Abs(float64(g) - float64(w)); d > peak {
					peak = d
				}
			}
		}
	}

	return peak
}

func hashFrames(f *Float) string {
	h := sha256.New()

	fmt.Fprintf(h, "%d %d %d %d\n", f.Width, f.Height, f.Channels, len(f.Frames))

	var b [4]byte

	for _, frame := range f.Frames {
		for _, v := range frame {
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
			h.Write(b[:])
		}
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

const streamRatchetPath = "testdata/streams.txt"

func TestAgainstReference(t *testing.T) {
	paths, err := filepath.Glob("testdata/streams/*.jxl")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no streams in testdata/streams")
	}

	prev := readStreamRatchet(t)
	fresh := map[string]string{}

	for _, path := range paths {
		name := filepath.Base(path)

		t.Run(name, func(t *testing.T) {
			got := decodeStream(t, path)
			ref := referencePath(path)

			if _, err := os.Stat(ref); err != nil {
				sum := hashFrames(got)
				fresh[name] = sum

				if want, ok := prev[name]; ok && want != sum {
					t.Errorf("output is %s, recorded %s", sum, want)
				} else {
					t.Logf("%s", sum)
				}

				return
			}

			peak := peakAgainst(t, got, ref)
			fresh[name] = strconv.FormatFloat(peak, 'g', -1, 64)

			limit := 1e-5

			if v, ok := prev[name]; ok {
				if limit, err = strconv.ParseFloat(v, 64); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}

			if peak > limit*1.25+1e-9 {
				t.Errorf("peak error %g against the reference, recorded %g", peak, limit)
			} else {
				t.Logf("peak error %g", peak)
			}
		})
	}

	if *updateRatchet {
		writeStreamRatchet(t, fresh)
	}
}

func readStreamRatchet(t *testing.T) map[string]string {
	t.Helper()

	m := map[string]string{}

	b, err := os.ReadFile(streamRatchetPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		return m
	}

	for line := range strings.Lines(string(b)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 2 {
			t.Fatalf("%s: want 'stream peak-or-hash', got %q", streamRatchetPath, line)
		}

		m[fields[0]] = fields[1]
	}

	return m
}

func writeStreamRatchet(t *testing.T, res map[string]string) {
	t.Helper()

	names := make([]string, 0, len(res))
	for k := range res {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# stream peak against testdata/streams/*.pfm.gz, or a hash of our own\n")
	b.WriteString("# output where there is no reference, from\n")
	b.WriteString("# go test -run TestAgainstReference -conformance.update\n")
	for _, n := range names {
		fmt.Fprintf(&b, "%s %s\n", n, res[n])
	}

	if err := os.WriteFile(streamRatchetPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func peakAgainstPNG(got image.Image, path string, premul bool) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	ref, err := png.Decode(f)
	if err != nil {
		return 0, err
	}

	gb, rb := got.Bounds(), ref.Bounds()
	if gb.Dx() != rb.Dx() || gb.Dy() != rb.Dy() {
		return 0, fmt.Errorf("%dx%d, the reference is %dx%d", gb.Dx(), gb.Dy(), rb.Dx(), rb.Dy())
	}

	var peak float64

	for y := range gb.Dy() {
		for x := range gb.Dx() {
			r1, g1, b1, a1 := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			r2, g2, b2, a2 := ref.At(rb.Min.X+x, rb.Min.Y+y).RGBA()

			if premul {
				c := color.NRGBA64Model.Convert(ref.At(rb.Min.X+x, rb.Min.Y+y)).(color.NRGBA64)
				a2 = uint32(c.A)
				r2, g2, b2 = min(uint32(c.R), a2), min(uint32(c.G), a2), min(uint32(c.B), a2)
			}

			for _, d := range [4]float64{
				math.Abs(float64(r1) - float64(r2)),
				math.Abs(float64(g1) - float64(g2)),
				math.Abs(float64(b1) - float64(b2)),
				math.Abs(float64(a1) - float64(a2)),
			} {
				peak = math.Max(peak, d/65535)
			}
		}
	}

	return peak, nil
}
