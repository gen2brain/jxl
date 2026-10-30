// Package jxl decodes and encodes JPEG XL images.
//
// Decoding covers both Modular and VarDCT codestreams. Encoding writes a
// single frame from sRGB input, lossy VarDCT by default and Modular when
// [EncodeOptions.Lossless] is set. An alpha channel is always kept losslessly.
//
// [DecodeExif] parses the Exif metadata of a file, while [RawExif] and
// [RawXMP] return the payloads whole. All three report [ErrNoExif] or
// [ErrNoXMP] when the file carries no such box, and [ErrUnsupported] when it
// carries one Brotli compressed.
package jxl

import (
	"errors"
	"image"
	"io"
	"math"
)

// ErrInvalid means the bitstream is malformed. ErrUnsupported means it is
// well-formed but uses a feature this decoder does not implement.
var (
	ErrInvalid     = errors.New("jxl: invalid bitstream")
	ErrUnsupported = errors.New("jxl: unsupported feature")
)

// ErrFrameTooLarge is returned when a frame's area exceeds
// Options.FrameSizeLimit.
var ErrFrameTooLarge = errors.New("jxl: frame exceeds size limit")

// DefaultLevel is the codestream level enforced when Options.Level is zero.
const DefaultLevel = 5

// DefaultFrameSizeLimit is the frame area, in pixels, accepted when
// Options.FrameSizeLimit is zero.
const DefaultFrameSizeLimit = min(16384*16384, math.MaxInt>>6)

// DefaultMaxFrames bounds the frames a decode reads from a codestream.
const DefaultMaxFrames = 1024

// Options configure a decode.
type Options struct {
	// Level is the codestream level to accept. Zero means DefaultLevel.
	Level int
	// Threads bounds the goroutines decoding one frame. Zero means GOMAXPROCS,
	// one decodes serially.
	Threads int
	// FrameSizeLimit bounds a frame's area in pixels, so that a corrupt or
	// hostile header cannot ask for an unbounded allocation. Zero means
	// DefaultFrameSizeLimit; a negative value removes the limit.
	FrameSizeLimit int
	// MaxFrames bounds the frames a decode reads from a codestream. Zero
	// means DefaultMaxFrames; a negative value removes the limit.
	MaxFrames int
}

func options(opts []Options) Options {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Level == 0 {
		o.Level = DefaultLevel
	}

	return o
}

// ColorInfo reports the color space of the decoded samples, as CICP code
// points or as the ICC profile the codestream carried.
type ColorInfo struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	ICCP      []byte
}

// JXL is every frame of a file, with the delay of each in seconds.
type JXL struct {
	Image []image.Image
	Delay []float64
	Loops int
	Color ColorInfo
}

// Float is the decoder's native output: interleaved float32 samples per frame,
// in the image's own color space.
type Float struct {
	Width    int
	Height   int
	Channels int
	// Frames each hold Width*Height*Channels samples, row-major.
	Frames [][]float32
	Color  ColorInfo
}

// Decode reads a JPEG XL image as *image.NRGBA, or *image.NRGBA64 above 8
// bits. Grayscale without alpha gives *image.Gray or *image.Gray16.
func Decode(r io.Reader, opts ...Options) (image.Image, error) {
	img, _, err := DecodeColor(r, opts...)

	return img, err
}

// DecodeColor reads a JPEG XL image and reports the color space it came from.
func DecodeColor(r io.Reader, opts ...Options) (image.Image, ColorInfo, error) {
	a, err := DecodeAll(r, opts...)
	if err != nil {
		return nil, ColorInfo{}, err
	}

	return a.Image[0], a.Color, nil
}

// DecodeAll reads every frame of a JPEG XL file. A still image gives one frame.
func DecodeAll(r io.Reader, opts ...Options) (*JXL, error) {
	return decodeAll(r, options(opts))
}

// DecodeFloat reads a JPEG XL file into the decoder's native float samples,
// without converting to an image.Image.
func DecodeFloat(r io.Reader, opts ...Options) (*Float, error) {
	return decodeFloat(r, options(opts))
}

const configChunk = 1 << 12

// DecodeConfig reads the dimensions and color model without decoding pixels.
func DecodeConfig(r io.Reader) (image.Config, error) {
	var buf []byte

	for size := configChunk; ; size *= 4 {
		have := len(buf)

		buf = append(buf, make([]byte, size-have)...)
		n, err := io.ReadFull(r, buf[have:])
		buf = buf[:have+n]

		cfg, cerr := decodeConfig(buf)
		if cerr == nil {
			return cfg, nil
		}

		if err != nil {
			return image.Config{}, cerr
		}
	}
}

func decode(r io.Reader) (image.Image, error) { return Decode(r) }

func init() {
	image.RegisterFormat("jxl", sigCodestream, decode, DecodeConfig)
	image.RegisterFormat("jxl", sigContainer, decode, DecodeConfig)
}
