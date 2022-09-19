//go:build cgo
// +build cgo

package jxl

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"unsafe"

	j40 "github.com/gen2brain/jxl/j40c"
)

func decode(r io.Reader, configOnly bool) (image.Image, image.Config, error) {
	var img j40.Image
	defer j40.Free(&img)

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, image.Config{}, err
	}

	ret := j40.FromMemory(&img, unsafe.Pointer(&data[0]), uint32(len(data)))
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", j40.ErrorString(&img))
	}

	ret = j40.OutputFormat(&img, j40.Rgba, j40.U8x4)
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", j40.ErrorString(&img))
	}

	if j40.NextFrame(&img) != 0 {
		frame := j40.CurrentFrame(&img)
		pixels := j40.FramePixels(frame, j40.Rgba)

		if configOnly {
			cfg := image.Config{}
			cfg.Width = int(pixels.Width)
			cfg.Height = int(pixels.Height)
			cfg.ColorModel = color.RGBAModel
			return nil, cfg, nil
		}

		b := image.Rect(0, 0, int(pixels.Width), int(pixels.Height))
		out := image.NewRGBA(b)
		out.Pix = unsafe.Slice((*byte)(pixels.Data), pixels.Height*pixels.Stride+pixels.Width)
		out.Stride = int(pixels.Stride)

		ret = j40.Error(&img)
		if ret != 0 {
			return nil, image.Config{}, fmt.Errorf("jxl: %s", j40.ErrorString(&img))
		}

		return out, image.Config{}, nil
	}

	ret = j40.Error(&img)
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", j40.ErrorString(&img))
	}

	return nil, image.Config{}, nil
}
