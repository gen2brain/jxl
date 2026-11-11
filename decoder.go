package jxl

import (
	"image"
	"io"
	"math"
	"slices"
)

type refFrame struct {
	w, h     int
	planes   [][]float32
	beforeCT bool
}

type decoder struct {
	opts     Options
	threads  int
	hdr      fileHeader
	iccp     []byte
	arena    arena
	src      *source
	used     int
	refs     [4]*canvas
	preCT    [4]*refFrame
	lfFrames [4]*refFrame

	visible    int
	nonvisible int
	frames     int
	preview    bool
}

func newDecoder(r io.Reader, o Options) (*decoder, error) {
	return newSourceDecoder(newSource(r, nil), o, false)
}

func newBytesDecoder(data []byte, o Options) (*decoder, error) {
	return newSourceDecoder(newSource(nil, data), o, false)
}

func newSourceDecoder(s *source, o Options, partial bool) (*decoder, error) {
	s.need(len(sigContainer))

	if s.fail != nil {
		return nil, s.fail
	}

	boxed, skip, err := signature(s.window())
	if err != nil {
		return nil, err
	}

	d := &decoder{opts: o, threads: numThreads(o.Threads)}

	level := 5

	if boxed {
		data, err := s.all()
		if err != nil {
			return nil, err
		}

		c, err := readContainer(data[skip:], partial)
		if err != nil {
			return nil, err
		}

		level = c.level
		s = newSource(nil, c.codestream)
	}

	if level > d.opts.Level {
		return nil, ErrUnsupported
	}

	for {
		br := newBitReader(s.window())

		var hdr fileHeader

		hdr.read(br)

		err := br.err()
		if err == nil {
			d.hdr = hdr

			if !partial && hdr.meta.color.wantICC {
				encoded := readICCStream(br)

				if err = br.err(); err == nil {
					profile, ok := decodeICC(encoded)
					if !ok {
						return nil, ErrInvalid
					}

					d.iccp = profile
				}
			}
		}

		if err == nil {
			if !levelAllows(d.opts.Level, &d.hdr, len(d.iccp)) {
				return nil, ErrUnsupported
			}

			if !partial {
				if err := d.checkFrameSize(d.hdr.size.xsize, d.hdr.size.ysize); err != nil {
					return nil, err
				}
			}

			d.src = s
			d.used = br.pos()

			return d, nil
		}

		if !s.more() {
			if s.fail != nil {
				return nil, s.fail
			}

			return nil, err
		}
	}
}

func (d *decoder) checkFrameSize(w, h uint32) error {
	lim := int64(d.opts.FrameSizeLimit)

	if lim == 0 {
		lim = DefaultFrameSizeLimit
	}

	if lim < 0 {
		return nil
	}

	if int64(w)*int64(h) > lim {
		return ErrFrameTooLarge
	}

	return nil
}

func (d *decoder) checkFrameCount() error {
	lim := d.opts.MaxFrames

	if lim == 0 {
		lim = DefaultMaxFrames
	}

	if lim > 0 && d.frames > lim {
		return ErrUnsupported
	}

	return nil
}

func levelAllows(level int, hdr *fileHeader, iccSize int) bool {
	x, y := uint64(hdr.size.xsize), uint64(hdr.size.ysize)
	icc := uint64(iccSize)
	channels := len(hdr.meta.extraChannels)

	if x > 1<<30 || y > 1<<30 || x*y > 1<<40 || icc > 1<<28 || channels > 256 {
		return false
	}

	if level >= 10 {
		return true
	}

	if x > 1<<18 || y > 1<<18 || x*y > 1<<28 || icc > 1<<22 || channels > 4 {
		return false
	}

	for i := range hdr.meta.extraChannels {
		if hdr.meta.extraChannels[i].ecType == ecBlack {
			return false
		}
	}

	return true
}

func decodeAll(r io.Reader, o Options) (*JXL, error) {
	d, err := newDecoder(r, o)
	if err != nil {
		return nil, err
	}

	out := &JXL{Loops: d.loops(), Color: d.colorInfo()}

	err = d.decodeFrames(true, func(s []float32, delay float64) (bool, error) {
		img, err := d.toImage(s)
		if err != nil {
			return false, err
		}

		out.Image = append(out.Image, img)
		out.Delay = append(out.Delay, delay)

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func decodeFirst(r io.Reader, o Options) (image.Image, ColorInfo, error) {
	d, err := newDecoder(r, o)
	if err != nil {
		return nil, ColorInfo{}, err
	}

	var img image.Image

	err = d.decodeFrames(true, func(s []float32, _ float64) (bool, error) {
		img, err = d.toImage(s)

		return false, err
	})
	if err != nil {
		return nil, ColorInfo{}, err
	}

	return img, d.colorInfo(), nil
}

func (d *decoder) colorInfo() ColorInfo {
	c := &d.hdr.meta.color

	info := ColorInfo{
		Primaries: uint16(c.prim),
		Transfer:  uint16(c.tf.tf),
		Matrix:    0,
		ICCP:      d.iccp,
	}

	if c.space == csGray {
		info.Matrix = 2

		if d.hdr.meta.xybEncoded && !c.wantICC && c.white != wpD65 {
			info.Transfer = uint16(tfLinear)
		}
	}

	return info
}

func decodeFloat(r io.Reader, o Options) (*Float, error) {
	d, err := newDecoder(r, o)
	if err != nil {
		return nil, err
	}

	return d.floats()
}

func decodeFloatBytes(data []byte, o Options) (*Float, error) {
	d, err := newBytesDecoder(data, o)
	if err != nil {
		return nil, err
	}

	return d.floats()
}

func (d *decoder) floats() (*Float, error) {
	w, h, channels := d.outputSize()
	fl := &Float{Width: w, Height: h, Channels: channels, Color: d.colorInfo()}

	err := d.decodeFrames(false, func(s []float32, _ float64) (bool, error) {
		fl.Frames = append(fl.Frames, s)

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return fl, nil
}

func (d *decoder) skipPreview() error {
	d.preview = true
	defer func() { d.preview = false }()

	for {
		r := newBitReader(d.src.window())
		r.skip(d.used)

		f, err := d.readFrameHeader(r)
		if err == nil {
			end := r.pos()/8 + int(f.toc.offsets[len(f.toc.offsets)-1])

			if d.src.avail() >= end {
				d.used = end * 8
				d.src.advance(end)
				d.used = 0

				return nil
			}

			err = ErrInvalid
		}

		if !d.src.more() {
			if d.src.fail != nil {
				return d.src.fail
			}

			return err
		}
	}
}

func (d *decoder) outputSize() (int, int, int) {
	m := &d.hdr.meta

	w, h := int(d.hdr.size.xsize), int(d.hdr.size.ysize)
	if m.orient.transposing() {
		w, h = h, w
	}

	return w, h, m.colorChannels() + len(m.extraChannels)
}

func (d *decoder) loops() int {
	if !d.hdr.meta.haveAnimation {
		return 0
	}

	return int(d.hdr.meta.anim.numLoops)
}

func (d *decoder) decodeFrames(reuse bool, emit func(samples []float32, delay float64) (bool, error)) error {
	m := &d.hdr.meta

	if m.havePreview {
		if err := d.skipPreview(); err != nil {
			return err
		}
	}

	w, h := int(d.hdr.size.xsize), int(d.hdr.size.ysize)
	ow, _, channels := d.outputSize()

	b := d.newBlender()

	var interleaved []float32

	emitted := 0

	for {
		f, err := d.nextFrame()
		if err != nil {
			return err
		}

		if f.hdr.isVisible() {
			d.visible++
			d.nonvisible = 0
		} else {
			d.nonvisible++
		}

		planes, err := d.renderFrame(f, channels)
		if err != nil {
			return err
		}

		if f.hdr.ftype == ftLF {
			continue
		}

		fw, fh := f.hdr.sizeUpsampled()

		colorSpec := specOf(f.hdr.blending)

		ecSpecs := make([]blendSpec, len(f.hdr.ecBlending))
		for i, e := range f.hdr.ecBlending {
			ecSpecs[i] = specOf(e)
		}

		var result *canvas

		if fw == w && fh == h && f.hdr.x0 == 0 && f.hdr.y0 == 0 &&
			allReplace(colorSpec, ecSpecs) {
			result = &canvas{w: w, h: h, channels: channels, planes: planes}
		} else {
			ref := d.refs[f.hdr.blending.source]
			blank := ref == nil

			if blank {
				ref = newCanvas(w, h, channels, &d.arena)
			}

			result = newCanvas(w, h, channels, &d.arena)

			b.blend(result, ref, planes, fw, fh,
				int(f.hdr.x0), int(f.hdr.y0), colorSpec, ecSpecs)

			if blank {
				d.arena.putAll(ref.planes)
			}

			d.arena.putAll(planes)
		}

		if f.hdr.canBeReferenced {
			old := d.refs[f.hdr.saveAsReference]
			d.refs[f.hdr.saveAsReference] = result
			d.release(old, result)
		}

		if f.hdr.isLast || f.hdr.duration > 0 {
			if !reuse || interleaved == nil {
				interleaved = make([]float32, w*h*channels)
			}

			if m.orient == orIdentity {
				overRows(d.threads, h, func(y0, y1 int) {
					lo, hi := y0*w, y1*w

					if channels == 1 {
						copy(interleaved[lo:hi], result.planes[0][lo:hi])

						return
					}

					for i := lo; i < hi; i++ {
						dst := interleaved[i*channels : (i+1)*channels]

						for c, p := range result.planes {
							dst[c] = p[i]
						}
					}
				})
			} else {
				overRows(d.threads, h, func(y0, y1 int) {
					for y := y0; y < y1; y++ {
						for x := range w {
							ox, oy := m.orient.displayPixel(x, y, w, h)
							dst := (oy*ow + ox) * channels

							for c := range channels {
								interleaved[dst+c] = result.planes[c][y*w+x]
							}
						}
					}
				})
			}

			emitted++

			more, err := emit(interleaved, f.hdr.delay(&m.anim))
			if err != nil {
				d.release(result, nil)

				return err
			}

			if !more {
				d.release(result, nil)

				return nil
			}
		}

		d.release(result, nil)

		if f.hdr.isLast {
			break
		}
	}

	if emitted == 0 {
		return ErrInvalid
	}

	return nil
}

func (d *decoder) nextFrame() (*frame, error) {
	var (
		f   *frame
		r   *bitReader
		err error
	)

	for {
		r = newBitReader(d.src.window())
		r.skip(d.used)

		if f, err = d.readFrameHeader(r); err == nil {
			if end := r.pos()/8 + int(f.toc.offsets[len(f.toc.offsets)-1]); d.src.avail() >= end {
				break
			}

			err = ErrInvalid
		}

		if !d.src.more() {
			if d.src.fail != nil {
				return nil, d.src.fail
			}

			return nil, err
		}
	}

	d.frames++

	if err := d.checkFrameCount(); err != nil {
		return nil, err
	}

	if err := d.decodeFrame(f, r); err != nil {
		return nil, err
	}

	d.used = r.pos()
	d.src.advance(d.used / 8)
	d.used %= 8

	return f, nil
}

func (d *decoder) release(c, keep *canvas) {
	if c == nil || c == keep {
		return
	}

	for _, r := range d.refs {
		if r == c {
			return
		}
	}

	d.arena.putAll(c.planes)
}

func (d *decoder) colorPlanes(f *frame) int {
	if d.hdr.meta.xybEncoded || f.vardct != nil {
		return 3
	}

	return d.hdr.meta.colorChannels()
}

func (d *decoder) renderFrame(f *frame, channels int) ([][]float32, error) {
	m := &d.hdr.meta
	cw, ch := f.hdr.size()
	w, h := f.hdr.sizeUpsampled()

	planes, err := d.toPlanes(f, cw, ch)
	if err != nil {
		return nil, err
	}

	color := d.colorPlanes(f)

	if g := &f.hdr.filter; g.gab {
		weights := [3][2]float32{
			{g.gabXWeight1, g.gabXWeight2},
			{g.gabYWeight1, g.gabYWeight2},
			{g.gabBWeight1, g.gabBWeight2},
		}

		scratch := d.arena.getDirty(cw * ch)

		for c := range min(color, 3) {
			out := gaborish(planes[c], scratch, cw, ch, d.threads, weights[c][0], weights[c][1])

			scratch, planes[c] = planes[c], out
		}

		d.arena.put(scratch)
	}

	if err := d.applyEPF(f, planes, cw, ch, color); err != nil {
		return nil, err
	}

	d.applyPatches(f, planes, cw, ch)

	if f.hdr.flags&flagSplines != 0 {
		f.splines.draw(planes, cw, ch)
	}

	if f.hdr.upsampling > 1 {
		planes = d.upsamplePlanes(f, planes, cw, ch, w, h)
	}

	if f.hdr.flags&flagNoise != 0 {
		d.addNoise(f, planes, w, h)
	}

	if f.hdr.ftype == ftLF {
		lf := &refFrame{w: w, h: h, planes: make([][]float32, 3)}

		for i := range 3 {
			lf.planes[i] = planes[i]
		}

		d.lfFrames[f.hdr.lfLevel-1] = lf

		return nil, nil
	}

	if f.hdr.canBeReferenced && f.hdr.saveBeforeCT {
		ref := &refFrame{w: w, h: h, beforeCT: true, planes: make([][]float32, len(planes))}

		for i, p := range planes {
			ref.planes[i] = make([]float32, len(p))
			copy(ref.planes[i], p)
		}

		d.preCT[f.hdr.saveAsReference] = ref
	}

	switch {
	case f.hdr.doYCbCr:
		ycbcrToRGB(planes, w, h, d.threads)
	case m.xybEncoded:
		if err := d.xybToRGB(planes, w, h); err != nil {
			return nil, err
		}
	}

	if len(planes) != channels {
		planes = append(planes[:m.colorChannels()], planes[color:]...)
	}

	return planes, nil
}

func (d *decoder) toPlanes(f *frame, w, h int) ([][]float32, error) {
	m := &d.hdr.meta
	color := d.colorPlanes(f)

	planes := make([][]float32, color+len(m.extraChannels))

	adopted := 0
	if f.vardct != nil {
		adopted = 3
	}

	for i := adopted; i < len(planes); i++ {
		planes[i] = d.arena.getDirty(w * h)
	}

	switch {
	case f.vardct != nil:
		v := f.vardct

		for c := range 3 {
			src := v.pixels[c]
			sw, sh := v.bw*blockDim>>v.hs[c], v.bh*blockDim>>v.vs[c]

			for range v.hs[c] {
				src = chromaUpsampleH(src, sw, sh)
				sw *= 2
			}

			for range v.vs[c] {
				src = chromaUpsampleV(src, sw, sh)
				sh *= 2
			}

			if sw != w {
				for y := 1; y < h; y++ {
					copy(src[y*w:(y+1)*w], src[y*sw:])
				}
			}

			planes[c] = src[:w*h]

			if &src[0] != &v.pixels[c][0] {
				d.arena.put(v.pixels[c])
			}

			v.pixels[c] = nil
		}
	case m.xybEncoded:
		if len(f.modular.channels) < 3 {
			return nil, ErrUnsupported
		}

		cy, cx, cb := f.modular.channels[0], f.modular.channels[1], f.modular.channels[2]
		if cy.w < w || cy.h < h || !cx.equivalent(cy) || !cb.equivalent(cy) {
			return nil, ErrUnsupported
		}

		s := f.lfQuant.factors

		overRows(d.threads, h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				ry, rx, rb := cy.row(y), cx.row(y), cb.row(y)

				for x := range w {
					i := y*w + x
					planes[0][i] = float32(rx[x]) * s[0]
					planes[1][i] = float32(ry[x]) * s[1]
					planes[2][i] = (float32(rb[x]) + float32(ry[x])) * s[2]
				}
			}
		})
	}

	mbase := color
	if f.vardct != nil {
		mbase = 0
	}

	for c := range planes {
		var (
			ch    *modChannel
			depth bitDepth
		)

		if c < color {
			if f.vardct != nil || m.xybEncoded {
				continue
			}

			ch, depth = f.modular.channels[c], m.depth
		} else {
			ch, depth = f.modular.channels[mbase+c-color], m.extraChannels[c-color].depth
		}

		if ch.w < w || ch.h < h {
			return nil, ErrUnsupported
		}

		if depth.floatSample {
			if depth.bitsPerSample != 32 || m.xybEncoded {
				return nil, ErrUnsupported
			}

			overRows(d.threads, h, func(y0, y1 int) {
				for y := y0; y < y1; y++ {
					row := ch.row(y)

					for x := range w {
						planes[c][y*w+x] = math.Float32frombits(uint32(row[x]))
					}
				}
			})

			continue
		}

		scale := float32(1) / float32(uint64(1)<<depth.bitsPerSample-1)

		overRows(d.threads, h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := ch.row(y)

				for x := range w {
					planes[c][y*w+x] = float32(row[x]) * scale
				}
			}
		})
	}

	return planes, nil
}

func (d *decoder) applyEPF(f *frame, planes [][]float32, w, h, color int) error {
	rf := &f.hdr.filter

	if rf.epfIters == 0 {
		return nil
	}

	e := epfPlanes{w: w, h: h, threads: d.threads, arena: &d.arena}

	switch {
	case color >= 3:
		e.planes = slices.Clone(planes[:3])
		e.scales = rf.epfChannelScale[:]
	case color == 1:
		e.planes = slices.Clone(planes[:1])
		e.scales = []float32{
			rf.epfChannelScale[0] + rf.epfChannelScale[1] + rf.epfChannelScale[2],
		}
	default:
		return ErrUnsupported
	}

	if f.vardct != nil {
		e.sigma, e.sw = f.vardct.sigmaImage(rf)
	} else {
		e.sw = divCeil(w, blockDim)
		e.sigma = make([]float32, e.sw*divCeil(h, blockDim))

		for i := range e.sigma {
			e.sigma[i] = epfInvSigmaNum / rf.epfSigmaForModular
		}
	}

	e.init()

	if rf.epfIters >= 3 {
		e.pass0(rf.epfPass0SigmaScale, rf.epfBorderSadMul)
	}

	e.pass1(1, rf.epfBorderSadMul)

	if rf.epfIters >= 2 {
		e.pass2(rf.epfPass2SigmaScale, rf.epfBorderSadMul)
	}

	copy(planes, e.planes)
	d.arena.putAll(e.out)

	return nil
}

func ycbcrToRGB(planes [][]float32, w, h, threads int) {
	const (
		c128  = 128.0 / 255
		crToR = 1.402
		crToG = -0.299 * 1.402 / 0.587
		cbToG = -0.114 * 1.772 / 0.587
		cbToB = 1.772
	)

	overRows(threads, h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			cb, y, cr := planes[0][i], planes[1][i]+c128, planes[2][i]

			planes[0][i] = cr*crToR + y
			planes[1][i] = cr*crToG + (cb*cbToG + y)
			planes[2][i] = cb*cbToB + y
		}
	})
}

func (d *decoder) xybToRGB(planes [][]float32, w, h int) error {
	intensity := d.hdr.meta.tone.intensityTarget
	p := newXYBParams(&d.hdr.transform.opsin, intensity)

	mat, out, ok := xybOutputFor(&d.hdr.meta.color, p.mat, intensity)
	if !ok {
		return ErrInvalid
	}

	p.mat = mat

	n := w * h
	x, y, b := planes[0][:n], planes[1][:n], planes[2][:n]

	linear := !out.tf.haveGamma && out.tf.tf == tfLinear
	srgb := !out.tf.haveGamma && out.tf.tf == tfSRGB

	var k *[29][8]float32

	var k512 *[29][16]float32

	switch {
	case srgb && xybSRGB512Asm != nil:
		k512 = p.srgbConsts512()
	case srgb && xybSRGBAsm != nil:
		k = p.srgbConsts()
	}

	overRows(d.threads, h, func(y0, y1 int) {
		lo, hi := y0*w, y1*w

		if k512 != nil {
			if m := (hi - lo) &^ 15; m > 0 {
				xybSRGB512Asm(&x[lo], &y[lo], &b[lo], m, k512)

				lo += m
			}
		}

		if k != nil {
			if m := (hi - lo) &^ 7; m > 0 {
				xybSRGBAsm(&x[lo], &y[lo], &b[lo], m, k)

				lo += m
			}
		}

		switch {
		case linear:
			for i := lo; i < hi; i++ {
				x[i], y[i], b[i] = p.apply(x[i], y[i], b[i])
			}
		case srgb:
			for i := lo; i < hi; i++ {
				r, g, bb := p.apply(x[i], y[i], b[i])

				x[i] = linearToSRGB(r)
				y[i] = linearToSRGB(g)
				b[i] = linearToSRGB(bb)
			}
		default:
			for i := lo; i < hi; i++ {
				r, g, bb := out.toScene(p.apply(x[i], y[i], b[i]))

				x[i] = out.encode(r)
				y[i] = out.encode(g)
				b[i] = out.encode(bb)
			}
		}
	})

	return nil
}

func decodeConfig(data []byte) (image.Config, error) {
	d, err := newSourceDecoder(newSource(nil, data), options([]Options{{Level: 10}}), true)
	if err != nil {
		return image.Config{}, err
	}

	w, h := int(d.hdr.size.xsize), int(d.hdr.size.ysize)
	if d.hdr.meta.orient.transposing() {
		w, h = h, w
	}

	return image.Config{Width: w, Height: h, ColorModel: d.hdr.meta.colorModel()}, nil
}
