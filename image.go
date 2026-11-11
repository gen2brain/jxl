package jxl

import (
	"image"
)

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}

	return v
}

func (d *decoder) renderSpot(f *Float, frame int) {
	m := &d.hdr.meta
	color := m.colorChannels()
	src := f.Frames[frame]

	for i, e := range m.extraChannels {
		if e.ecType != ecSpotColor {
			continue
		}

		scale := e.spotColor[3]

		overRows(d.threads, f.Height, func(y0, y1 int) {
			for j := y0 * f.Width; j < y1*f.Width; j++ {
				o := j * f.Channels
				mix := scale * src[o+color+i]

				for c := range min(color, 3) {
					src[o+c] = mix*e.spotColor[c] + (1-mix)*src[o+c]
				}
			}
		})
	}
}

func (d *decoder) toImage(f *Float, frame int) (image.Image, error) {
	m := &d.hdr.meta
	gray := m.color.space == csGray
	alpha := m.alphaIndex()
	color := m.colorChannels()

	d.renderSpot(f, frame)

	deep := m.depth.bitsPerSample > 8 || m.depth.floatSample
	premul := false

	if alpha >= 0 {
		ec := &m.extraChannels[alpha]
		deep = deep || ec.depth.bitsPerSample > 8 || ec.depth.floatSample
		premul = ec.alphaAssociated
	}

	rect := image.Rect(0, 0, f.Width, f.Height)
	src := f.Frames[frame]

	ci := [3]int{0, 1, 2}
	if gray {
		ci = [3]int{}
	}

	ai := -1
	if alpha >= 0 {
		ai = color + alpha
	}

	n := f.Channels

	switch {
	case gray && alpha < 0 && !deep:
		img := image.NewGray(rect)

		overRows(d.threads, f.Height, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src[y*f.Width*n:]
				p := img.Pix[y*img.Stride:]

				for x := range f.Width {
					p[x] = to8(row[x*n])
				}
			}
		})

		return img, nil

	case gray && alpha < 0:
		img := image.NewGray16(rect)

		overRows(d.threads, f.Height, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src[y*f.Width*n:]
				p := img.Pix[y*img.Stride:]

				for x := range f.Width {
					v := to16(row[x*n])
					p[x*2] = uint8(v >> 8)
					p[x*2+1] = uint8(v)
				}
			}
		})

		return img, nil

	case !deep:
		img := image.NewNRGBA(rect)

		overRows(d.threads, f.Height, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src[y*f.Width*n:]
				p := img.Pix[y*img.Stride:]

				for x := range f.Width {
					o, q := x*n, p[x*4:]

					k := float32(1)
					if premul {
						k = unpremul(row[o+ai])
					}

					q[0] = to8(row[o+ci[0]] * k)
					q[1] = to8(row[o+ci[1]] * k)
					q[2] = to8(row[o+ci[2]] * k)

					q[3] = 255
					if ai >= 0 {
						q[3] = to8(row[o+ai])
					}
				}
			}
		})

		return img, nil
	}

	img := image.NewNRGBA64(rect)

	overRows(d.threads, f.Height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := src[y*f.Width*n:]
			p := img.Pix[y*img.Stride:]

			for x := range f.Width {
				o, q := x*n, p[x*8:]

				k := float32(1)
				if premul {
					k = unpremul(row[o+ai])
				}

				for c := range 3 {
					v := to16(row[o+ci[c]] * k)
					q[c*2] = uint8(v >> 8)
					q[c*2+1] = uint8(v)
				}

				v := uint16(65535)
				if ai >= 0 {
					v = to16(row[o+ai])
				}

				q[6] = uint8(v >> 8)
				q[7] = uint8(v)
			}
		}
	})

	return img, nil
}

func unpremul(a float32) float32 {
	return 1 / max(1.0/(1<<26), a)
}

func to8(v float32) uint8 {
	return uint8(float64(clamp01(v))*255 + 0.5)
}

func to16(v float32) uint16 {
	return uint16(float64(clamp01(v))*65535 + 0.5)
}
