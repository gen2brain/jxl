package jxl

import (
	"image"
	"io"
)

// Decode reads a JXL image from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	m, _, err := decode(r, false)
	if err != nil {
		return nil, err
	}
	return m, err
}

// DecodeConfig returns the color model and dimensions of a JXL image without
// decoding the entire image.
func DecodeConfig(r io.Reader) (image.Config, error) {
	_, c, err := decode(r, true)
	return c, err
}

func init() {
	image.RegisterFormat("jxl", "\xff\x0a", Decode, DecodeConfig)
}
