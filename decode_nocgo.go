//go:build !cgo
// +build !cgo

package jxl

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"unsafe"

	"github.com/gen2brain/jxl/j40"
	"modernc.org/libc"
)

func decode(r io.Reader, configOnly bool) (image.Image, image.Config, error) {
	tls := libc.NewTLS()
	defer tls.Close()

	var img j40.J40_image
	imgptr := uintptr(unsafe.Pointer(&img))
	defer j40.J40_free(tls, imgptr)

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, image.Config{}, err
	}

	ret := j40.J40_from_memory(tls, imgptr, uintptr(unsafe.Pointer(&data[0])), uint64(len(data)), 0)
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", libc.GoString(j40.J40_error_string(tls, imgptr)))
	}

	ret = j40.J40_output_format(tls, imgptr, j40.J40_RGBA, j40.J40_U8X4)
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", libc.GoString(j40.J40_error_string(tls, imgptr)))
	}

	if j40.J40_next_frame(tls, imgptr) != 0 {
		frame := j40.J40_current_frame(tls, imgptr)
		pixels := j40.J40_frame_pixels_u8x4(tls, uintptr(unsafe.Pointer(&frame)), j40.J40_RGBA)

		ret = j40.J40_error(tls, imgptr)
		if ret != 0 {
			return nil, image.Config{}, fmt.Errorf("jxl: %s", libc.GoString(j40.J40_error_string(tls, imgptr)))
		}

		if configOnly {
			cfg := image.Config{}
			cfg.Width = int(pixels.Width)
			cfg.Height = int(pixels.Height)
			cfg.ColorModel = color.RGBAModel
			return nil, cfg, nil
		}

		b := image.Rect(0, 0, int(pixels.Width), int(pixels.Height))
		out := image.NewRGBA(b)
		out.Pix = libc.GoBytes(pixels.Data, int(pixels.Height*pixels.Stride_bytes))
		out.Stride = int(pixels.Stride_bytes)

		return out, image.Config{}, nil
	}

	ret = j40.J40_error(tls, imgptr)
	if ret != 0 {
		return nil, image.Config{}, fmt.Errorf("jxl: %s", libc.GoString(j40.J40_error_string(tls, imgptr)))
	}

	return nil, image.Config{}, fmt.Errorf("jxl: image not decoded")
}
