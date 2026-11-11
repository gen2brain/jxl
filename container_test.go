package jxl

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func box(typ string, body ...[]byte) []byte {
	content := bytes.Join(body, nil)
	b := make([]byte, 8, 8+len(content))

	binary.BigEndian.PutUint32(b, uint32(8+len(content)))
	copy(b[4:], typ)

	return append(b, content...)
}

func boxLarge(typ string, body ...[]byte) []byte {
	content := bytes.Join(body, nil)
	b := make([]byte, 16, 16+len(content))

	binary.BigEndian.PutUint32(b, 1)
	copy(b[4:], typ)
	binary.BigEndian.PutUint64(b[8:], uint64(16+len(content)))

	return append(b, content...)
}

func ftyp() []byte {
	return box("ftyp", []byte("jxl "), []byte{0, 0, 0, 0}, []byte("jxl "))
}

func ftypV1() []byte {
	return box("ftyp", []byte("jxl "), []byte{0, 0, 0, 1}, []byte("jxl "))
}

func jxlp(index uint32, last bool, body []byte) []byte {
	if last {
		index |= 0x80000000
	}

	idx := make([]byte, 4)
	binary.BigEndian.PutUint32(idx, index)

	return box("jxlp", idx, body)
}

func TestParseContainer(t *testing.T) {
	stream := []byte("\xff\x0acodestream")

	tests := []struct {
		name string
		in   []byte
		want []byte
		err  bool
	}{
		{
			name: "jxlc",
			in:   bytes.Join([][]byte{ftyp(), box("jxlc", stream)}, nil),
			want: stream,
		},
		{
			name: "jxlc with 64-bit size",
			in:   bytes.Join([][]byte{ftyp(), boxLarge("jxlc", stream)}, nil),
			want: stream,
		},
		{
			name: "jxlp in order",
			in: bytes.Join([][]byte{ftyp(),
				jxlp(0, false, stream[:5]), jxlp(1, false, stream[5:9]),
				jxlp(2, true, stream[9:])}, nil),
			want: stream,
		},
		{
			name: "jxlp out of order, version 1",
			in: bytes.Join([][]byte{ftypV1(),
				jxlp(2, true, stream[9:]), jxlp(0, false, stream[:5]),
				jxlp(1, false, stream[5:9])}, nil),
			want: stream,
		},
		{
			name: "metadata between codestream boxes",
			in: bytes.Join([][]byte{ftyp(),
				jxlp(0, false, stream[:5]), box("Exif", []byte("exifdata")),
				box("xml ", []byte("<x/>")), jxlp(1, true, stream[5:])}, nil),
			want: stream,
		},
		{
			name: "unknown box is skipped",
			in: bytes.Join([][]byte{ftyp(), box("zzzz", []byte("whatever")),
				box("jxlc", stream)}, nil),
			want: stream,
		},
		{
			name: "jbrd is skipped, not refused",
			in: bytes.Join([][]byte{ftyp(), box("jxlc", stream),
				box("jbrd", []byte("reconstruction"))}, nil),
			want: stream,
		},

		{
			name: "no ftyp",
			in:   box("jxlc", stream),
			err:  true,
		},
		{
			name: "ftyp is not first",
			in:   bytes.Join([][]byte{box("jxlc", stream), ftyp()}, nil),
			err:  true,
		},
		{
			name: "wrong brand",
			in: bytes.Join([][]byte{box("ftyp", []byte("avif"), []byte{0, 0, 0, 0}),
				box("jxlc", stream)}, nil),
			err: true,
		},
		{
			name: "future ftyp version",
			in: bytes.Join([][]byte{box("ftyp", []byte("jxl "), []byte{0, 0, 0, 2}),
				box("jxlc", stream)}, nil),
			err: true,
		},
		{
			name: "two ftyp",
			in:   bytes.Join([][]byte{ftyp(), ftyp(), box("jxlc", stream)}, nil),
			err:  true,
		},
		{
			name: "no codestream",
			in:   bytes.Join([][]byte{ftyp(), box("Exif", []byte("x"))}, nil),
			err:  true,
		},
		{
			name: "jxlc after jxlp",
			in: bytes.Join([][]byte{ftyp(), jxlp(0, false, stream[:5]),
				box("jxlc", stream)}, nil),
			err: true,
		},
		{
			name: "jxlp after jxlc",
			in: bytes.Join([][]byte{ftyp(), box("jxlc", stream),
				jxlp(0, true, stream)}, nil),
			err: true,
		},
		{
			name: "jxlp after the last one",
			in: bytes.Join([][]byte{ftyp(), jxlp(0, true, stream),
				jxlp(1, true, stream)}, nil),
			err: true,
		},
		{
			name: "jxlp with a repeated index",
			in: bytes.Join([][]byte{ftypV1(), jxlp(0, false, stream[:5]),
				jxlp(0, true, stream[5:])}, nil),
			err: true,
		},
		{
			name: "jxlp with a gap",
			in: bytes.Join([][]byte{ftypV1(), jxlp(0, false, stream[:5]),
				jxlp(2, true, stream[5:])}, nil),
			err: true,
		},
		{
			name: "jxlp out of order, version 0",
			in: bytes.Join([][]byte{ftyp(), jxlp(1, true, stream[5:]),
				jxlp(0, false, stream[:5])}, nil),
			err: true,
		},
		{
			name: "jxlp never marked last",
			in: bytes.Join([][]byte{ftyp(), jxlp(0, false, stream[:5]),
				jxlp(1, false, stream[5:])}, nil),
			err: true,
		},
		{
			name: "box size below its header",
			in:   bytes.Join([][]byte{ftyp(), {0, 0, 0, 4, 'j', 'x', 'l', 'c'}}, nil),
			err:  true,
		},
		{
			name: "box size past the end",
			in:   bytes.Join([][]byte{ftyp(), {0, 0, 0xff, 0, 'j', 'x', 'l', 'c'}}, nil),
			err:  true,
		},
		{
			name: "truncated box header",
			in:   bytes.Join([][]byte{ftyp(), {0, 0, 0}}, nil),
			err:  true,
		},
		{
			name: "64-bit size announced but absent",
			in:   bytes.Join([][]byte{ftyp(), {0, 0, 0, 1, 'j', 'x', 'l', 'c'}}, nil),
			err:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := readContainer(tt.in, false)

			if tt.err {
				if err == nil {
					t.Fatal("no error")
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(c.codestream, tt.want) {
				t.Errorf("codestream = %q, want %q", c.codestream, tt.want)
			}
		})
	}
}

func TestParseContainerSizeZero(t *testing.T) {
	stream := []byte("\xff\x0acodestream")

	in := append(ftyp(), append([]byte{0, 0, 0, 0, 'j', 'x', 'l', 'c'}, stream...)...)

	c, err := readContainer(in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.codestream, stream) {
		t.Errorf("codestream = %q, want %q", c.codestream, stream)
	}
}

func TestParseContainerMetadata(t *testing.T) {
	stream := []byte("\xff\x0acodestream")

	in := bytes.Join([][]byte{ftyp(),
		box("jxll", []byte{10}),
		box("Exif", []byte("exifdata")),
		box("xml ", []byte("<x/>")),
		box("jumb", []byte("jumbf")),
		box("brob", []byte("xml "), []byte("compressed")),
		box("jxlc", stream),
	}, nil)

	c, err := readContainer(in, false)
	if err != nil {
		t.Fatal(err)
	}

	if c.level != 10 {
		t.Errorf("level = %d, want 10", c.level)
	}
	if string(c.exif) != "exifdata" {
		t.Errorf("exif = %q", c.exif)
	}
	if string(c.xmp) != "<x/>" {
		t.Errorf("xmp = %q", c.xmp)
	}
	if string(c.jumb) != "jumbf" {
		t.Errorf("jumb = %q", c.jumb)
	}
	if len(c.compressed) != 1 || string(c.compressed["xml "]) != "compressed" {
		t.Errorf("compressed = %q, want one xml box", c.compressed)
	}
}

func TestContainerCorpus(t *testing.T) {
	root := conformanceRoot(t)

	paths, err := filepath.Glob(filepath.Join(root, "testcases", "*", "input.jxl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no inputs found")
	}

	var boxed int

	for _, path := range paths {
		name := filepath.Base(filepath.Dir(path))

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			isContainer, skip, err := signature(data)
			if err != nil {
				t.Fatal(err)
			}
			if !isContainer {
				return
			}

			boxed++

			c, err := readContainer(data[skip:], false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(c.codestream, []byte(sigCodestream)) {
				t.Fatalf("extracted codestream starts %x, want %x",
					c.codestream[:min(2, len(c.codestream))], sigCodestream)
			}
			for typ, body := range c.compressed {
				if _, err := brotliDecode(body, maxMetadataSize); err != nil {
					t.Errorf("brob %q: %v", typ, err)
				}
			}
		})
	}

	if boxed == 0 {
		t.Error("no container-format inputs in the corpus")
	}

	t.Logf("%d of %d inputs use the container format", boxed, len(paths))
}

type shortReader struct {
	data []byte
	pos  int
	read int
}

func (s *shortReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}

	n := copy(p, s.data[s.pos:])
	s.pos += n
	s.read += n

	return n, nil
}

func TestDecodeConfigReadsAHeader(t *testing.T) {
	var paths []string

	for _, g := range []string{
		filepath.Join("testdata", "*", "*.jxl"),
		filepath.Join(conformanceRoot(t), "testcases", "*", "input.jxl"),
	} {
		m, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}

		paths = append(paths, m...)
	}

	if len(paths) == 0 {
		t.Fatal("no inputs found")
	}

	checked := 0

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}

		want, werr := decodeConfig(data)

		r := &shortReader{data: data}

		got, err := DecodeConfig(r)
		if !errors.Is(err, werr) {
			t.Fatalf("%s: streamed error %v, whole file %v", p, err, werr)
		}
		if werr != nil {
			continue
		}
		if got != want {
			t.Fatalf("%s: streamed %v, whole file %v", p, got, want)
		}

		checked++

		if len(data) > 4*configChunk && r.read > configChunk {
			t.Errorf("%s: read %d of %d bytes for a header", p, r.read, len(data))
		}
	}

	if checked == 0 {
		t.Fatal("no inputs produced a config")
	}

	t.Logf("%d inputs, each answered from at most %d bytes", checked, configChunk)
}

func FuzzContainer(f *testing.F) {
	stream := []byte("\xff\x0acodestream")

	f.Add(bytes.Join([][]byte{ftyp(), box("jxlc", stream)}, nil))
	f.Add(bytes.Join([][]byte{ftypV1(), jxlp(1, true, stream[5:]),
		jxlp(0, false, stream[:5])}, nil))
	f.Add(bytes.Join([][]byte{ftyp(), box("brob", []byte("Exif"), []byte("x")),
		boxLarge("jxlc", stream)}, nil))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := readContainer(data, false)
		if err != nil {
			return
		}
		if c.codestream == nil {
			t.Fatal("no error but no codestream")
		}
	})
}

func FuzzDecode(f *testing.F) {
	for _, g := range []string{"testdata/streams/*.jxl", "testdata/bench/*.jxl"} {
		paths, err := filepath.Glob(g)
		if err != nil {
			f.Fatal(err)
		}

		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil {
				f.Add(b)
			}
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		opt := Options{Threads: 1, FrameSizeLimit: 1 << 21, MaxFrames: 5}

		img, err := Decode(bytes.NewReader(data), opt)
		if err == nil && img == nil {
			t.Fatal("no error but no image")
		}

		if _, err := DecodeConfig(bytes.NewReader(data)); err != nil {
			return
		}

		if _, err := DecodeAll(bytes.NewReader(data), opt); err != nil {
			return
		}

		if _, err := DecodeFloat(bytes.NewReader(data), opt); err != nil {
			t.Fatal("DecodeAll succeeded but DecodeFloat did not")
		}
	})
}

func TestFrameSizeLimit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "streams", "dct_epf3.jxl"))
	if err != nil {
		t.Skip(err)
	}

	cfg, err := DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	area := cfg.Width * cfg.Height

	for _, tc := range []struct {
		limit int
		want  error
	}{
		{0, nil},
		{area, nil},
		{area - 1, ErrFrameTooLarge},
		{1, ErrFrameTooLarge},
		{-1, nil},
	} {
		_, err := Decode(bytes.NewReader(data), Options{Level: 10, FrameSizeLimit: tc.limit})

		if !errors.Is(err, tc.want) {
			t.Fatalf("limit %d over %d pixels gave %v, want %v", tc.limit, area, err, tc.want)
		}
	}
}

func layeredStream(t *testing.T, canvas uint32, n int) []byte {
	t.Helper()

	const w, h = 8, 8

	src := imageSamples(image.NewGray(image.Rect(0, 0, w, h)))

	var head fileHeader

	head.size = sizeHeader{xsize: canvas, ysize: canvas}
	head.meta = defaultImageMetadata()
	head.meta.xybEncoded = false
	head.meta.depth = bitDepth{bitsPerSample: src.bits}
	head.transform = defaultCustomTransformData()

	var out bitWriter

	head.write(&out)

	for i := range n {
		f := newModularFrame(&head.meta, w, h)
		f.hdr.isLast = i == n-1
		f.hdr.haveCrop = canvas != w
		f.hdr.frameWidth, f.hdr.frameHeight = w, h
		f.setModularSamples(&src, w, h)

		body, err := f.encodeModularSections()
		if err != nil {
			t.Fatal(err)
		}

		f.encode(&out, &head.meta, canvas, canvas, body)
	}

	if err := out.err(); err != nil {
		t.Fatal(err)
	}

	return out.bytes()
}

func TestMaxFrames(t *testing.T) {
	const n = 6

	data := layeredStream(t, 8, n)

	for _, tc := range []struct {
		limit int
		want  error
	}{
		{0, nil},
		{n, nil},
		{n - 1, ErrUnsupported},
		{1, ErrUnsupported},
		{-1, nil},
	} {
		opt := Options{MaxFrames: tc.limit}

		if _, err := DecodeAll(bytes.NewReader(data), opt); !errors.Is(err, tc.want) {
			t.Fatalf("DecodeAll of %d frames under a bound of %d gave %v, want %v", n, tc.limit, err, tc.want)
		}

		if _, err := DecodeFloat(bytes.NewReader(data), opt); !errors.Is(err, tc.want) {
			t.Fatalf("DecodeFloat of %d frames under a bound of %d gave %v, want %v", n, tc.limit, err, tc.want)
		}
	}
}

func TestFrameSizeLimitCanvas(t *testing.T) {
	const canvas = 64

	data := layeredStream(t, canvas, 1)

	for _, tc := range []struct {
		limit int
		want  error
	}{
		{canvas * canvas, nil},
		{canvas*canvas - 1, ErrFrameTooLarge},
		{8 * 8, ErrFrameTooLarge},
	} {
		fl, err := DecodeFloat(bytes.NewReader(data), Options{FrameSizeLimit: tc.limit})
		if !errors.Is(err, tc.want) {
			t.Fatalf("an 8x8 frame on a %dx%d canvas under a limit of %d gave %v, want %v",
				canvas, canvas, tc.limit, err, tc.want)
		}

		if err == nil && (fl.Width != canvas || fl.Height != canvas) {
			t.Fatalf("decoded %dx%d, want %dx%d", fl.Width, fl.Height, canvas, canvas)
		}
	}
}

func TestImageAlpha(t *testing.T) {
	samples := []float32{0.25, 0.5, 0.1, 0.5, 0.2, 0.2, 0.2, 0, 0.9, 0.3, 0.6, 1}

	for _, tc := range []struct {
		name       string
		alphaBits  uint32
		associated bool
		want       [3]color.NRGBA64
	}{
		{"straight 8", 8, false, [3]color.NRGBA64{
			{0x4040, 0x8080, 0x1a1a, 0x8080}, {0x3333, 0x3333, 0x3333, 0}, {0xe5e5, 0x4d4d, 0x9999, 0xffff},
		}},
		{"straight 16", 16, false, [3]color.NRGBA64{
			{0x4000, 0x8000, 0x199a, 0x8000}, {0x3333, 0x3333, 0x3333, 0}, {0xe665, 0x4ccd, 0x9999, 0xffff},
		}},
		{"associated 16", 16, true, [3]color.NRGBA64{
			{0x8000, 0xffff, 0x3333, 0x8000}, {0xffff, 0xffff, 0xffff, 0}, {0xe665, 0x4ccd, 0x9999, 0xffff},
		}},
	} {
		d := &decoder{threads: 1}
		d.hdr.meta = defaultImageMetadata()
		d.hdr.meta.depth = bitDepth{bitsPerSample: 8}

		ec := defaultExtraChannelInfo()
		ec.depth = bitDepth{bitsPerSample: tc.alphaBits}
		ec.alphaAssociated = tc.associated
		d.hdr.meta.extraChannels = []extraChannelInfo{ec}

		img, err := d.toImage(&Float{Width: 3, Height: 1, Channels: 4, Frames: [][]float32{samples}}, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		for x, want := range tc.want {
			if got := rawNRGBA64(img, x, 0); got != want {
				t.Fatalf("%s: %T pixel %d is %v, want %v", tc.name, img, x, got, want)
			}
		}
	}
}

type tiffTag struct {
	tag, typ uint16
	count    uint32
	data     []byte
}

func tiffLen(tags []tiffTag) int {
	n := 2 + len(tags)*12 + 4
	for _, t := range tags {
		if len(t.data) > 4 {
			n += len(t.data)
		}
	}

	return n
}

// tiffIFD encodes an IFD placed at base, pointing values too big to inline at
// the overflow area that follows the directory.
func tiffIFD(base int, tags []tiffTag) []byte {
	over := base + 2 + len(tags)*12 + 4

	dir := binary.BigEndian.AppendUint16(nil, uint16(len(tags)))
	var extra []byte
	for _, t := range tags {
		dir = binary.BigEndian.AppendUint16(dir, t.tag)
		dir = binary.BigEndian.AppendUint16(dir, t.typ)
		dir = binary.BigEndian.AppendUint32(dir, t.count)
		if len(t.data) > 4 {
			dir = binary.BigEndian.AppendUint32(dir, uint32(over+len(extra)))
			extra = append(extra, t.data...)
		} else {
			v := make([]byte, 4)
			copy(v, t.data)
			dir = append(dir, v...)
		}
	}
	dir = binary.BigEndian.AppendUint32(dir, 0)

	return append(dir, extra...)
}

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func rat(n, d uint32) []byte {
	return append(be32(n), be32(d)...)
}

// sampleTIFF builds the payload the Exif tests read back.
func sampleTIFF() []byte {
	gps := []tiffTag{
		{tagGPSLatitudeRef, typeASCIIString, 2, []byte("N\x00")},
		{tagGPSLatitude, typeUnsignedRational, 3, slices.Concat(rat(48, 1), rat(51, 1), rat(1234, 100))},
		{tagGPSLongitudeRef, typeASCIIString, 2, []byte("W\x00")},
		{tagGPSLongitude, typeUnsignedRational, 3, slices.Concat(rat(2, 1), rat(21, 1), rat(50, 100))},
		{tagGPSAltitudeRef, typeUnsignedByte, 1, []byte{1}},
		{tagGPSAltitude, typeUnsignedRational, 1, rat(3500, 100)},
	}
	sub := []tiffTag{
		{tagExposureTime, typeUnsignedRational, 1, rat(1, 250)},
		{tagFNumber, typeUnsignedRational, 1, rat(56, 10)},
		{tagISOSpeedRatings, typeUnsignedShort, 1, be16(800)},
		{tagDateTimeOriginal, typeASCIIString, 20, []byte("2024:03:01 09:15:00\x00")},
		{tagFlash, typeUnsignedShort, 1, be16(9)},
		{tagFocalLength, typeUnsignedRational, 1, rat(350, 10)},
	}

	ifd0Base := 8
	var ifd0 []tiffTag
	build := func() []byte {
		subBase := ifd0Base + tiffLen(ifd0)
		gpsBase := subBase + tiffLen(sub)

		return slices.Concat(
			[]byte("MM\x00\x2a"), be32(uint32(ifd0Base)),
			tiffIFD(ifd0Base, ifd0),
			tiffIFD(subBase, sub),
			tiffIFD(gpsBase, gps),
		)
	}

	ifd0 = []tiffTag{
		{tagImageWidth, typeUnsignedLong, 1, be32(1600)},
		{tagImageLength, typeUnsignedShort, 1, be16(900)},
		{tagMake, typeASCIIString, 6, []byte("Canon\x00")},
		{tagModel, typeASCIIString, 8, []byte("EOS 5D\x00\x00")},
		{tagSoftware, typeASCIIString, 8, []byte("jxl\x00\x00\x00")},
		{tagDateTime, typeASCIIString, 20, []byte("2024:03:02 11:00:00\x00")},
		{tagArtist, typeASCIIString, 6, []byte("milan\x00")},
		{tagCopyright, typeASCIIString, 4, []byte("CC0\x00")},
		{tagOrientation, typeUnsignedShort, 1, be16(6)},
		{tagExifIFDPointer, typeUnsignedLong, 1, nil},
		{tagGPSIFDPointer, typeUnsignedLong, 1, nil},
	}
	subBase := ifd0Base + tiffLen(ifd0)
	ifd0[len(ifd0)-2].data = be32(uint32(subBase))
	ifd0[len(ifd0)-1].data = be32(uint32(subBase + tiffLen(sub)))

	return build()
}

// exifFile wraps a TIFF payload in the boxes a JPEG XL file carries.
func exifFile(tiff []byte, offset uint32, extra ...[]byte) []byte {
	pad := make([]byte, offset)

	parts := [][]byte{
		[]byte(sigContainer),
		ftyp(),
		box("Exif", be32(offset), pad, tiff),
		box("xml ", []byte("<x:xmpmeta/>")),
	}

	parts = append(parts, extra...)
	parts = append(parts, box("jxlc", []byte("\xff\x0acodestream")))

	return bytes.Join(parts, nil)
}

func TestExif(t *testing.T) {
	e, err := DecodeExif(bytes.NewReader(exifFile(sampleTIFF(), 0)))
	if err != nil {
		t.Fatal(err)
	}

	// Indexed so the degrees round exactly as the parser rounds them.
	dms := []float64{48, 51, 12.34, 2, 21, 0.5}

	want := Exif{
		Orientation: 6, Width: 1600, Height: 900,
		Make: "Canon", Model: "EOS 5D", Software: "jxl",
		DateTime: "2024:03:02 11:00:00", DateTimeOriginal: "2024:03:01 09:15:00",
		ExposureTime: 1.0 / 250, FNumber: 5.6, ISOSpeed: 800, FocalLength: 35, Flash: 9,
		GPSLatitude:  dms[0] + dms[1]/60 + dms[2]/3600,
		GPSLongitude: -(dms[3] + dms[4]/60 + dms[5]/3600),
		GPSAltitude:  -35,
		Copyright:    "CC0", Artist: "milan",
	}

	if *e != want {
		t.Fatalf("got %+v\nwant %+v", *e, want)
	}
}

// TestExifTIFFOffset covers the exif_tiff_header_offset, including a writer
// that gets it wrong, which the scan recovers from.
func TestExifTIFFOffset(t *testing.T) {
	for _, offset := range []uint32{0, 6, 64} {
		e, err := DecodeExif(bytes.NewReader(exifFile(sampleTIFF(), offset)))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}

		if e.Orientation != 6 {
			t.Fatalf("offset %d: orientation %d", offset, e.Orientation)
		}
	}

	file := exifFile(sampleTIFF(), 6)
	binary.BigEndian.PutUint32(file[len(sigContainer)+len(ftyp())+8:], 999)

	e, err := DecodeExif(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("wrong offset: %v", err)
	}

	if e.Orientation != 6 {
		t.Fatalf("wrong offset: orientation %d", e.Orientation)
	}
}

func TestExifXMP(t *testing.T) {
	xmp, err := RawXMP(bytes.NewReader(exifFile(sampleTIFF(), 0)))
	if err != nil {
		t.Fatal(err)
	}

	if string(xmp) != "<x:xmpmeta/>" {
		t.Fatalf("xmp is %q", xmp)
	}
}

// brotliStored wraps bytes in a brotli stream of one uncompressed meta-block,
// which needs no compressor to build.
func brotliStored(data []byte) []byte {
	var w bitWriter

	w.bit(false)
	w.bit(false)
	w.bits(2, 0)
	w.bits(16, uint64(len(data)-1))
	w.bit(true)
	w.zeroPad()

	for _, b := range data {
		w.bits(8, uint64(b))
	}

	w.bit(true)
	w.bit(true)

	return w.bytes()
}

// TestExifCompressed reads metadata out of the brob boxes libjxl writes by
// default, which carry the payload brotli compressed.
func TestExifCompressed(t *testing.T) {
	tiff := sampleTIFF()

	file := bytes.Join([][]byte{
		[]byte(sigContainer),
		ftyp(),
		box("brob", []byte("Exif"), brotliStored(append(be32(0), tiff...))),
		box("brob", []byte("xml "), brotliStored([]byte("<x:xmpmeta/>"))),
		box("jxlc", []byte("\xff\x0acodestream")),
	}, nil)

	e, err := DecodeExif(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}

	if e.Orientation != 6 || e.Make != "Canon" {
		t.Fatalf("orientation %d make %q", e.Orientation, e.Make)
	}

	xmp, err := RawXMP(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}

	if string(xmp) != "<x:xmpmeta/>" {
		t.Fatalf("xmp is %q", xmp)
	}
}

func TestExifMissing(t *testing.T) {
	bare := []byte("\xff\x0acodestream")
	plain := bytes.Join([][]byte{[]byte(sigContainer), ftyp(), box("jxlc", bare)}, nil)

	brob := bytes.Join([][]byte{
		[]byte(sigContainer),
		ftyp(),
		box("brob", []byte("Exif"), []byte("not brotli")),
		box("brob", []byte("xml "), []byte("not brotli")),
		box("jxlc", bare),
	}, nil)

	for _, tc := range []struct {
		name      string
		data      []byte
		exif, xmp error
	}{
		{"bare codestream", bare, ErrNoExif, ErrNoXMP},
		{"no metadata boxes", plain, ErrNoExif, ErrNoXMP},
	} {
		if _, err := DecodeExif(bytes.NewReader(tc.data)); !errors.Is(err, tc.exif) {
			t.Fatalf("%s: DecodeExif gave %v, want %v", tc.name, err, tc.exif)
		}

		if _, err := RawXMP(bytes.NewReader(tc.data)); !errors.Is(err, tc.xmp) {
			t.Fatalf("%s: RawXMP gave %v, want %v", tc.name, err, tc.xmp)
		}
	}

	if _, err := DecodeExif(bytes.NewReader(brob)); err == nil {
		t.Fatal("a brob box that is not brotli was accepted")
	}

	if _, err := DecodeExif(bytes.NewReader([]byte("not a jxl"))); err == nil {
		t.Fatal("a file that is not JPEG XL was accepted")
	}
}

func rawNRGBA64(img image.Image, x, y int) color.NRGBA64 {
	switch m := img.(type) {
	case *image.NRGBA:
		c := m.NRGBAAt(x, y)

		return color.NRGBA64{uint16(c.R) * 257, uint16(c.G) * 257, uint16(c.B) * 257, uint16(c.A) * 257}
	case *image.NRGBA64:
		return m.NRGBA64At(x, y)
	}

	return color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
}
