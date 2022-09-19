// Package j40 is independent, self-contained JPEG XL decoder.
package j40

/*
#include "j40.h"
#include <stdlib.h>

#cgo CFLAGS: -std=gnu99 -DJ40_CONFIRM_THAT_THIS_IS_EXPERIMENTAL_AND_POTENTIALLY_UNSAFE
#cgo LDFLAGS: -lm
*/
import "C"
import (
	"unsafe"
)

// Constants .
const (
	U8x4 = 0x0f33
	Rgba = 0x1755
)

// Err .
type Err uint32

// Image .
type Image struct {
	Magic uint32
	_     [4]byte
	_     struct{ inner uintptr }
}

// newImageFromPointer returns new Image from pointer.
func newImageFromPointer(ptr unsafe.Pointer) *Image {
	return (*Image)(ptr)
}

// cptr returns C pointer.
func (i *Image) cptr() *C.j40_image {
	return (*C.j40_image)(unsafe.Pointer(i))
}

// Frame .
type Frame struct {
	Magic    uint32
	Reserved uint32
	_        uintptr
}

// newFrameFromPointer returns new Frame from pointer.
func newFrameFromPointer(ptr unsafe.Pointer) *Frame {
	return (*Frame)(ptr)
}

// cptr returns C pointer.
func (f *Frame) cptr() *C.j40_frame {
	return (*C.j40_frame)(unsafe.Pointer(f))
}

// Pixels .
type Pixels struct {
	Width  int32
	Height int32
	Stride int32
	Data   unsafe.Pointer
}

// newPixelsFromPointer returns new Pixels from pointer.
func newPixelsFromPointer(ptr unsafe.Pointer) Pixels {
	return *(*Pixels)(ptr)
}

// cptr returns C pointer.
func (p *Pixels) cptr() C.j40_pixels_u8x4 {
	return *(*C.j40_pixels_u8x4)(unsafe.Pointer(p))
}

// Error .
func Error(image *Image) Err {
	cimage := image.cptr()
	ret := C.j40_error(cimage)
	v := (Err)(ret)
	return v
}

// ErrorString .
func ErrorString(image *Image) string {
	cimage := image.cptr()
	ret := C.j40_error_string(cimage)
	v := C.GoString(ret)
	return v
}

// FromMemory .
func FromMemory(image *Image, buf unsafe.Pointer, size uint32) Err {
	cimage := image.cptr()
	csize := (C.size_t)(size)
	ret := C.j40_from_memory(cimage, buf, csize, nil)
	v := (Err)(ret)
	return v
}

// FromFile .
func FromFile(image *Image, path string) Err {
	cimage := image.cptr()
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	ret := C.j40_from_file(cimage, cpath)
	v := (Err)(ret)
	return v
}

// OutputFormat .
func OutputFormat(image *Image, channel int32, format int32) Err {
	cimage := image.cptr()
	cchannel := (C.int32_t)(channel)
	cformat := (C.int32_t)(format)
	ret := C.j40_output_format(cimage, cchannel, cformat)
	v := (Err)(ret)
	return v
}

// NextFrame .
func NextFrame(image *Image) int32 {
	cimage := image.cptr()
	ret := C.j40_next_frame(cimage)
	v := (int32)(ret)
	return v
}

// CurrentFrame .
func CurrentFrame(image *Image) *Frame {
	cimage := image.cptr()
	ret := C.j40_current_frame(cimage)
	v := newFrameFromPointer(unsafe.Pointer(&ret))
	return v
}

// FramePixels .
func FramePixels(frame *Frame, channel int32) Pixels {
	cframe := frame.cptr()
	cchannel := (C.int32_t)(channel)
	ret := C.j40_frame_pixels_u8x4(cframe, cchannel)
	v := newPixelsFromPointer(unsafe.Pointer(&ret))
	return v
}

// Row .
func Row(pixels Pixels, y int32) []byte {
	cpixels := pixels.cptr()
	cy := (C.int32_t)(y)
	ret := C.j40_row_u8x4(cpixels, cy)
	v := C.GoBytes(unsafe.Pointer(&ret), 4)
	return v
}

// Free .
func Free(image *Image) {
	cimage := image.cptr()
	C.j40_free(cimage)
}
