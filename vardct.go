package jxl

import "math"

const (
	nonZeroBuckets          = 37
	zeroDensityContextCount = 458
	zeroDensityContextLimit = 474
	colorTileDim            = 64
	colorTileBlocks         = colorTileDim / blockDim
	defaultColorFactor      = 84
	invalidTransform        = uint8(numHFTransforms)
)

type quantizerParams struct {
	globalScale uint32
	quantLF     uint32
}

func (q *quantizerParams) read(r *bitReader) {
	switch r.bits(2) {
	case 0:
		q.globalScale = uint32(r.bits(11)) + 1
	case 1:
		q.globalScale = uint32(r.bits(11)) + 2049
	case 2:
		q.globalScale = uint32(r.bits(12)) + 4097
	default:
		q.globalScale = uint32(r.bits(16)) + 8193
	}

	switch r.bits(2) {
	case 0:
		q.quantLF = 16
	case 1:
		q.quantLF = uint32(r.bits(5)) + 1
	case 2:
		q.quantLF = uint32(r.bits(8)) + 1
	default:
		q.quantLF = uint32(r.bits(16)) + 1
	}
}

func (q *quantizerParams) invGlobalScale() float32 {
	return globalScaleDenom / float32(q.globalScale)
}

type colorCorrelation struct {
	factor uint32
	baseX  float32
	baseB  float32
	ytoxLF int32
	ytobLF int32
}

func (c *colorCorrelation) read(r *bitReader) {
	*c = colorCorrelation{factor: defaultColorFactor, baseB: 1}

	if r.bit() {
		return
	}

	switch r.bits(2) {
	case 0:
		c.factor = defaultColorFactor
	case 1:
		c.factor = 256
	case 2:
		c.factor = uint32(r.bits(8)) + 2
	default:
		c.factor = uint32(r.bits(16)) + 258
	}

	c.baseX = r.f16()
	c.baseB = r.f16()

	if c.baseX > 4 || c.baseB > 4 {
		r.setErr(ErrInvalid)

		return
	}

	c.ytoxLF = int32(r.bits(8)) - 128
	c.ytobLF = int32(r.bits(8)) - 128
}

func (c *colorCorrelation) yToX(f int32) float32 {
	return c.baseX + float32(f)/float32(c.factor)
}

func (c *colorCorrelation) yToB(f int32) float32 {
	return c.baseB + float32(f)/float32(c.factor)
}

var coeffFreqContext = [64]int{
	0xBAD, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 15, 16, 16, 17, 17, 18, 18, 19,
	19, 20, 20, 21, 21, 22, 22, 23, 23, 23, 23, 24, 24, 24, 24, 25, 25, 25, 25, 26, 26, 26, 26, 27,
	27, 27, 27, 28, 28, 28, 28, 29, 29, 29, 29, 30, 30, 30, 30,
}

var coeffNumNonzeroContext = [64]int{
	0xBAD, 0, 31, 62, 62, 93, 93, 93, 93, 123, 123, 123, 123, 152, 152, 152, 152, 152, 152, 152,
	152, 180, 180, 180, 180, 180, 180, 180, 180, 180, 180, 180, 180, 206, 206, 206, 206, 206, 206,
	206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206, 206,
	206, 206, 206, 206, 206, 206,
}

func shiftRightCeil(v, s int) int {
	return (v + (1 << s) - 1) >> s
}

func zeroDensityContext(nonzerosLeft, k, logNumBlocks, prev int) int {
	n := shiftRightCeil(nonzerosLeft, logNumBlocks)
	kn := k >> logNumBlocks

	return (coeffNumNonzeroContext[n&63]+coeffFreqContext[kn&63])*2 + prev
}

type blockContextMap struct {
	lfThresholds  [3][]int32
	qfThresholds  []uint32
	contextMap    []uint8
	numLFContexts int
	numContexts   int
}

var defaultBlockContextMap = []uint8{
	0, 1, 2, 2, 3, 3, 4, 5, 6, 6, 6, 6, 6,
	7, 8, 9, 9, 10, 11, 12, 13, 14, 14, 14, 14, 14,
	7, 8, 9, 9, 10, 11, 12, 13, 14, 14, 14, 14, 14,
}

func (b *blockContextMap) read(r *bitReader) {
	*b = blockContextMap{}

	if r.bit() {
		b.contextMap = defaultBlockContextMap
		b.numLFContexts = 1
		b.numContexts = 15

		return
	}

	b.numLFContexts = 1

	for c := range 3 {
		n := int(r.bits(4))
		v := make([]int32, n)

		for i := range v {
			var u uint32

			switch r.bits(2) {
			case 0:
				u = uint32(r.bits(4))
			case 1:
				u = uint32(r.bits(8)) + 16
			case 2:
				u = uint32(r.bits(16)) + 272
			default:
				u = uint32(r.bits(32)) + 65808
			}

			v[i] = unpackSigned(u)
		}

		b.lfThresholds[c] = v
		b.numLFContexts *= n + 1
	}

	nqf := int(r.bits(4))
	b.qfThresholds = make([]uint32, nqf)

	for i := range b.qfThresholds {
		var u uint32

		switch r.bits(2) {
		case 0:
			u = uint32(r.bits(2))
		case 1:
			u = uint32(r.bits(3)) + 4
		case 2:
			u = uint32(r.bits(5)) + 12
		default:
			u = uint32(r.bits(8)) + 44
		}

		b.qfThresholds[i] = u + 1
	}

	if b.numLFContexts*(nqf+1) > 64 {
		r.setErr(ErrInvalid)

		return
	}

	size := 3 * numOrders * b.numLFContexts * (nqf + 1)

	b.contextMap = decodeContextMap(r, size)

	if r.err() != nil {
		return
	}

	m := uint8(0)
	for _, v := range b.contextMap {
		m = max(m, v)
	}

	b.numContexts = int(m) + 1

	if b.numContexts > 16 {
		r.setErr(ErrInvalid)
	}
}

func (b *blockContextMap) numACContexts() int {
	return b.numContexts * (nonZeroBuckets + zeroDensityContextCount)
}

func (b *blockContextMap) blockContext(lfIdx int, qf uint32, shapeID, c int) int {
	qfIdx := 0

	for _, t := range b.qfThresholds {
		if qf > t {
			qfIdx++
		}
	}

	idx := 2
	if c < 2 {
		idx = c ^ 1
	}

	idx = idx*numOrders + shapeID
	idx = idx*(len(b.qfThresholds)+1) + qfIdx
	idx = idx*b.numLFContexts + lfIdx

	return int(b.contextMap[idx])
}

func (b *blockContextMap) nonzeroContext(nonzeros, blockContext int) int {
	ctx := 36

	switch {
	case nonzeros < 8:
		ctx = nonzeros
	case nonzeros < 64:
		ctx = 4 + nonzeros/2
	}

	return ctx*b.numContexts + blockContext
}

func (b *blockContextMap) zeroDensityOffset(blockContext int) int {
	return b.numContexts*nonZeroBuckets + zeroDensityContextCount*blockContext
}

type varDCTPass struct {
	orders [3 * numOrders][]uint32
	hist   histograms
}

type varDCT struct {
	quant quantizerParams
	cmap  colorCorrelation
	bctx  blockContextMap
	dq    dequantMatrices

	numHistograms uint32
	passes        []varDCTPass

	bw, bh int
	cw, ch int
	hs, vs [3]int

	lf         [3][]float32
	quantLF    []uint8
	ytox       []int8
	ytob       []int8
	rawQuant   []int32
	quantField []float32
	transform  []uint8
	epf        []uint8

	pixels [3][]float32
	biases [4]float32

	xdm, bdm float32
}

type dctScratch struct {
	coeffs   [3][]int32
	block    [3][]float32
	scratch  []float32
	lfBuf    []float32
	nonzeros [][3][]uint32
	passes   []acPass
}

func (s *dctScratch) alloc(f *frame) {
	dim := f.hdr.groupDim()
	nb := dim >> 3

	if len(s.coeffs[0]) == dim*dim && len(s.passes) == len(f.vardct.passes) &&
		(len(s.nonzeros) == 0 || len(s.nonzeros[0][0]) == nb*nb) {
		return
	}

	for c := range 3 {
		s.coeffs[c] = make([]int32, dim*dim)
		s.block[c] = make([]float32, maxCoeffArea)
	}

	s.scratch = make([]float32, maxCoeffArea+2*maxBlockDim)
	s.lfBuf = make([]float32, maxCoeffBlocks*maxCoeffBlocks)
	s.passes = make([]acPass, len(f.vardct.passes))

	s.nonzeros = make([][3][]uint32, len(f.vardct.passes))
	for i := range s.nonzeros {
		for c := range 3 {
			s.nonzeros[i][c] = make([]uint32, nb*nb)
		}
	}
}

func newVarDCT(f *frame, biases [4]float32, a *arena) *varDCT {
	v := &varDCT{biases: biases}

	v.bw, v.bh = f.hdr.sizeBlocks()
	v.cw, v.ch = divCeil(v.bw, 8), divCeil(v.bh, 8)

	n := v.bw * v.bh

	for c := range 3 {
		v.hs[c] = int(f.hdr.maxhs - jpegHShift[f.hdr.jpegUpsampling[c]])
		v.vs[c] = int(f.hdr.maxvs - jpegVShift[f.hdr.jpegUpsampling[c]])

		v.lf[c] = make([]float32, (v.bw>>v.hs[c])*(v.bh>>v.vs[c]))
		v.pixels[c] = a.get((v.bw * blockDim >> v.hs[c]) * (v.bh * blockDim >> v.vs[c]))
	}

	v.quantLF = make([]uint8, n)
	v.rawQuant = make([]int32, n)
	v.epf = make([]uint8, n)
	v.transform = make([]uint8, n)

	for i := range v.transform {
		v.transform[i] = invalidTransform
	}

	v.ytox = make([]int8, v.cw*v.ch)
	v.ytob = make([]int8, v.cw*v.ch)

	return v
}

func (f *frame) lfGroupBlockRect(g int) rect {
	lfx, _ := f.hdr.sizeLFGroups()
	dim := f.hdr.groupDim()
	gx, gy := g%lfx, g/lfx

	x0, y0 := gx*dim, gy*dim

	return rect{x0, y0, min(f.vardct.bw-x0, dim), min(f.vardct.bh-y0, dim)}
}

func (f *frame) blockGroupRect(g int) rect {
	gx, _ := f.hdr.sizeGroups()
	dim := f.hdr.groupDim() >> 3
	x0, y0 := (g%gx)*dim, (g/gx)*dim

	return rect{x0, y0, min(f.vardct.bw-x0, dim), min(f.vardct.bh-y0, dim)}
}

func (f *frame) decodeVarDCTLF(r *bitReader, g int, globalTree *maTree) {
	v := f.vardct

	extra := r.bits(2)
	mul := 1 / float32(int32(1)<<extra)

	rc := f.lfGroupBlockRect(g)

	var img modImage

	img.bitDepth = f.modular.bitDepth

	for _, c := range [3]int{1, 0, 2} {
		img.channels = append(img.channels,
			newModChannel(rc.w>>v.hs[c], rc.h>>v.vs[c], 0, 0))
	}

	img.decodeStream(r, nil, globalTree, 1+g)

	if r.err() != nil {
		return
	}

	invQuantLF := float32(globalScaleDenom) / (float32(v.quant.globalScale) * float32(v.quant.quantLF))

	var fac [3]float32

	for c := range 3 {
		fac[c] = f.lfQuant.factors[c] * invQuantLF * mul
	}

	if f.hdr.is444() {
		cflX := v.cmap.yToX(v.cmap.ytoxLF)
		cflB := v.cmap.yToB(v.cmap.ytobLF)

		for y := range rc.h {
			qy, qx, qb := img.channels[0].row(y), img.channels[1].row(y), img.channels[2].row(y)
			off := (rc.y0+y)*v.bw + rc.x0

			for x := range rc.w {
				inX := float32(qx[x]) * fac[0]
				inY := float32(qy[x]) * fac[1]
				inB := float32(qb[x]) * fac[2]

				v.lf[1][off+x] = inY
				v.lf[0][off+x] = inY*cflX + inX
				v.lf[2][off+x] = inY*cflB + inB
			}
		}
	} else {
		for c := range 3 {
			src := img.channels[2]
			if c < 2 {
				src = img.channels[c^1]
			}

			stride := v.bw >> v.hs[c]
			x0, y0 := rc.x0>>v.hs[c], rc.y0>>v.vs[c]

			for y := range rc.h >> v.vs[c] {
				row := src.row(y)
				off := (y0+y)*stride + x0

				for x := range rc.w >> v.hs[c] {
					v.lf[c][off+x] = float32(row[x]) * fac[c]
				}
			}
		}
	}

	if v.bctx.numLFContexts <= 1 {
		return
	}

	for y := range rc.h {
		qy := img.channels[0].row(y >> v.vs[1])
		qx := img.channels[1].row(y >> v.vs[0])
		qb := img.channels[2].row(y >> v.vs[2])
		off := (rc.y0+y)*v.bw + rc.x0

		for x := range rc.w {
			bucket := countAbove(v.bctx.lfThresholds[0], qx[x>>v.hs[0]])
			bucket *= len(v.bctx.lfThresholds[2]) + 1
			bucket += countAbove(v.bctx.lfThresholds[2], qb[x>>v.hs[2]])
			bucket *= len(v.bctx.lfThresholds[1]) + 1
			bucket += countAbove(v.bctx.lfThresholds[1], qy[x>>v.hs[1]])

			v.quantLF[off+x] = uint8(bucket)
		}
	}
}

func countAbove(thresholds []int32, v int32) int {
	n := 0

	for _, t := range thresholds {
		if v > t {
			n++
		}
	}

	return n
}

func (f *frame) decodeHFMetadata(r *bitReader, g int, globalTree *maTree) {
	v := f.vardct
	rc := f.lfGroupBlockRect(g)

	count := int(r.bits(ceilLog2(uint32(rc.w*rc.h)))) + 1

	cw, ch := divCeil(rc.w, 8), divCeil(rc.h, 8)

	var img modImage

	img.bitDepth = f.modular.bitDepth
	img.channels = []*modChannel{
		newModChannel(cw, ch, 3, 3),
		newModChannel(cw, ch, 3, 3),
		newModChannel(count, 2, 0, 0),
		newModChannel(rc.w, rc.h, 0, 0),
	}

	img.decodeStream(r, nil, globalTree, 1+2*f.hdr.numLFGroups()+g)

	if r.err() != nil {
		return
	}

	cx0, cy0 := rc.x0>>3, rc.y0>>3

	for y := range ch {
		rx, rb := img.channels[0].row(y), img.channels[1].row(y)
		off := (cy0+y)*v.cw + cx0

		for x := range cw {
			v.ytox[off+x] = int8(min(max(rx[x], -128), 127))
			v.ytob[off+x] = int8(min(max(rb[x], -128), 127))
		}
	}

	tr := img.channels[2]
	epf := img.channels[3]
	num := 0

	for y := range rc.h {
		erow := epf.row(y)
		off := (rc.y0+y)*v.bw + rc.x0

		for x := range rc.w {
			ev := erow[x]
			if ev < 0 || ev >= 8 {
				r.setErr(ErrInvalid)

				return
			}

			v.epf[off+x] = uint8(ev)

			if v.transform[off+x] != invalidTransform {
				continue
			}

			if num >= count {
				r.setErr(ErrInvalid)

				return
			}

			raw := tr.row(0)[num]
			quant := 1 + min(max(tr.row(1)[num], 0), 255)

			if raw < 0 || raw >= int32(numHFTransforms) {
				r.setErr(ErrInvalid)

				return
			}

			t := hfTransform(raw)
			cx, cy := t.blocks()

			nx, ny := (x/32+1)*32, (y/32+1)*32

			if x+cx > min(rc.w, nx) || y+cy > min(rc.h, ny) {
				r.setErr(ErrInvalid)

				return
			}

			num++

			for iy := range cy {
				trow := (rc.y0+y+iy)*v.bw + rc.x0 + x

				for ix := range cx {
					id := uint8(raw)
					if ix == 0 && iy == 0 {
						id |= 128
					}

					v.transform[trow+ix] = id
					v.rawQuant[trow+ix] = quant
				}
			}
		}
	}
}

func (f *frame) decodeHFGlobal(r *bitReader, globalTree *maTree) {
	v := f.vardct

	v.dq.read(r, f, globalTree)

	if r.err() != nil {
		return
	}

	v.numHistograms = uint32(r.bits(ceilLog2(uint32(f.hdr.numGroups())))) + 1

	numContexts := int(v.numHistograms) * v.bctx.numACContexts()

	v.passes = make([]varDCTPass, f.hdr.pass.numPasses)

	for i := range v.passes {
		p := &v.passes[i]

		var used uint32

		switch r.bits(2) {
		case 0:
			used = 0x5f
		case 1:
			used = 0x13
		case 2:
			used = 0
		default:
			used = uint32(r.bits(numOrders))
		}

		for o := range numOrders {
			for c := range 3 {
				p.orders[3*o+c] = naturalOrders[o]
			}
		}

		if used != 0 {
			var h histograms

			h.read(r, numPermutationContexts, true)

			if r.err() != nil {
				return
			}

			var sr symbolReader

			sr.init(&h, r, 0)

			for o := range numOrders {
				if used&(1<<o) == 0 {
					continue
				}

				t := orderTransform[o]
				cx, cy := t.blocks()
				size := uint32(cx * cy * blockSize)

				for c := range 3 {
					perm := decodePermutation(size, uint32(cx*cy), &h, r, &sr)

					if r.err() != nil {
						return
					}

					out := make([]uint32, size)
					copy(out, naturalOrders[o])
					composePermutation(out, perm)

					p.orders[3*o+c] = out
				}
			}

			if err := sr.checkFinalState(&h, r); err != nil {
				r.setErr(err)

				return
			}
		}

		p.hist.read(r, numContexts, true)

		if r.err() != nil {
			return
		}

		p.hist.resize(numContexts + zeroDensityContextLimit - zeroDensityContextCount)
	}

	v.xdm = powf(1/1.25, float32(f.hdr.xQMScale)-2)
	v.bdm = powf(1/1.25, float32(f.hdr.bQMScale)-2)
}

type acPass struct {
	r       *bitReader
	sr      symbolReader
	histIdx int
	shift   uint32
}

func (f *frame) decodeVarDCTGroup(w *worker, g int, readers []*bitReader) error {
	v := f.vardct
	s := &w.dct
	rc := f.blockGroupRect(g)

	numHistoBits := ceilLog2(v.numHistograms)

	passes := s.passes[:len(readers)]

	for i, br := range readers {
		p := &passes[i]
		p.r = br
		p.histIdx = int(br.bits(numHistoBits))

		if p.histIdx >= int(v.numHistograms) {
			return ErrInvalid
		}

		p.sr.init(&v.passes[i].hist, br, 0)

		if err := br.err(); err != nil {
			return err
		}

		if i < len(f.hdr.pass.shift) {
			p.shift = f.hdr.pass.shift[i]
		}

		for c := range 3 {
			clear(s.nonzeros[i][c])
		}
	}

	invGlobalScale := v.quant.invGlobalScale()

	coeffs, block := &s.coeffs, &s.block
	scratch, lfBuf := s.scratch, s.lfBuf

	offset := 0

	var sbx, sby [3]int

	for by := range rc.h {
		ty := (rc.y0 + by) / colorTileBlocks

		for c := range 3 {
			sby[c] = by >> v.vs[c]
		}

		for bx := range rc.w {
			tx := (rc.x0 + bx) / colorTileBlocks

			for c := range 3 {
				sbx[c] = bx >> v.hs[c]
			}
			pos := (rc.y0+by)*v.bw + rc.x0 + bx

			raw := v.transform[pos]
			if raw < 128 {
				continue
			}

			t := hfTransform(raw & 127)
			cx, cy := t.blocks()
			shapeID := int(hfShapeID[t])

			numBlocks := cx * cy
			numCoeffs := numBlocks * blockSize
			logBlocks := ceilLog2(uint32(numBlocks))

			rawQuant := uint32(v.rawQuant[pos])
			quantLF := int(v.quantLF[pos])

			for i := range passes {
				p := &passes[i]
				ps := &v.passes[i]
				ctxOffset := p.histIdx * v.bctx.numACContexts()

				for _, c := range [3]int{1, 0, 2} {
					if sbx[c]<<v.hs[c] != bx || sby[c]<<v.vs[c] != by {
						continue
					}

					nz := s.nonzeros[i][c]
					stride := rc.w >> v.hs[c]
					predicted := predictNonzeros(nz, stride, sbx[c], sby[c])
					bc := v.bctx.blockContext(quantLF, rawQuant, shapeID, c)
					nzCtx := v.bctx.nonzeroContext(predicted, bc) + ctxOffset

					nonzeros := int(p.sr.readUnsigned(&ps.hist, p.r, nzCtx))

					if err := p.r.err(); err != nil {
						return err
					}

					if nonzeros+numBlocks > numCoeffs {
						return ErrInvalid
					}

					count := uint32(shiftRightCeil(nonzeros, logBlocks))

					for iy := range cy {
						row := nz[(sby[c]+iy)*stride:]

						for ix := range cx {
							row[sbx[c]+ix] = count
						}
					}

					histOffset := v.bctx.zeroDensityOffset(bc) + ctxOffset

					prev := 1
					if nonzeros > numCoeffs/16 {
						prev = 0
					}

					order := ps.orders[shapeID*3+c]
					cur := coeffs[c][offset : offset+numCoeffs]

					for k := numBlocks; k < numCoeffs && nonzeros > 0; k++ {
						ctx := histOffset + zeroDensityContext(nonzeros, k, logBlocks, prev)
						coeff := p.sr.readSigned(&ps.hist, p.r, ctx) << p.shift

						prev = 0
						if coeff != 0 {
							prev = 1
						}

						nonzeros -= prev
						cur[order[k]] += coeff
					}

					if err := p.r.err(); err != nil {
						return err
					}

					if nonzeros != 0 {
						return ErrInvalid
					}
				}
			}

			xcc := v.cmap.yToX(int32(v.ytox[ty*v.cw+tx]))
			bcc := v.cmap.yToB(int32(v.ytob[ty*v.cw+tx]))

			v.dequantBlock(s, t, invGlobalScale, rawQuant, xcc, bcc, numCoeffs, offset)

			for _, c := range [3]int{1, 0, 2} {
				if sbx[c]<<v.hs[c] != bx || sby[c]<<v.vs[c] != by {
					continue
				}

				lfStride := v.bw >> v.hs[c]
				lfx := (rc.x0 + bx) >> v.hs[c]
				lfy := (rc.y0 + by) >> v.vs[c]

				for y := range cy {
					copy(lfBuf[y*cx:(y+1)*cx], v.lf[c][(lfy+y)*lfStride+lfx:])
				}

				transformToPixels(t, lfBuf, block[c][:numCoeffs], scratch)

				w, h := cx*blockDim, cy*blockDim
				stride := v.bw * blockDim >> v.hs[c]
				px := (rc.x0 + bx) * blockDim >> v.hs[c]
				py := (rc.y0 + by) * blockDim >> v.vs[c]

				for y := range h {
					copy(v.pixels[c][(py+y)*stride+px:][:w], block[c][y*w:])
				}
			}

			offset += numCoeffs
		}
	}

	for c := range 3 {
		clear(coeffs[c][:offset])
	}

	for i := range passes {
		if err := passes[i].sr.checkFinalState(&v.passes[i].hist, passes[i].r); err != nil {
			return err
		}
	}

	return nil
}

func predictNonzeros(m []uint32, stride, bx, by int) int {
	switch {
	case bx == 0 && by == 0:
		return 32
	case bx == 0:
		return int(m[(by-1)*stride])
	case by == 0:
		return int(m[by*stride+bx-1])
	}

	return int((m[(by-1)*stride+bx] + m[by*stride+bx-1] + 1) / 2)
}

func adjustQuantBias(c int, q int32, biases *[4]float32) float32 {
	if q == 0 {
		return 0
	}

	if q == -1 {
		return -biases[c]
	}

	if q == 1 {
		return biases[c]
	}

	v := float32(q)

	return v - biases[3]/v
}

func (v *varDCT) dequantBlock(s *dctScratch, t hfTransform, invGlobalScale float32, quant uint32,
	xcc, bcc float32, numCoeffs, offset int,
) {
	biases := &v.biases

	scaledY := invGlobalScale / float32(quant)
	scaledX := scaledY * v.xdm
	scaledB := scaledY * v.bdm

	coeffs, out := &s.coeffs, &s.block

	mx := v.dq.matrix(t, 0)
	my := v.dq.matrix(t, 1)
	mb := v.dq.matrix(t, 2)

	k := 0

	if n := numCoeffs &^ 7; n > 0 {
		a := dequantArgs{
			out:    [3]*float32{&out[0][0], &out[1][0], &out[2][0]},
			coeffs: [3]*int32{&coeffs[0][offset], &coeffs[1][offset], &coeffs[2][offset]},
			mat:    [3]*float32{&mx[0], &my[0], &mb[0]},
			n:      n,
			p: [9]float32{biases[0], biases[1], biases[2], biases[3],
				scaledX, scaledY, scaledB, xcc, bcc},
		}

		if dequant(&a) {
			k = n
		}
	}

	for ; k < numCoeffs; k++ {
		qx := adjustQuantBias(0, coeffs[0][offset+k], biases)
		qy := adjustQuantBias(1, coeffs[1][offset+k], biases)
		qb := adjustQuantBias(2, coeffs[2][offset+k], biases)

		dy := qy * my[k] * scaledY

		out[0][k] = xcc*dy + qx*mx[k]*scaledX
		out[1][k] = dy
		out[2][k] = bcc*dy + qb*mb[k]*scaledB
	}
}

func powf(a, b float32) float32 {
	return float32(math.Pow(float64(a), float64(b)))
}

func sqrtf(v float32) float32 { return float32(math.Sqrt(float64(v))) }
func expf(v float32) float32  { return float32(math.Exp(float64(v))) }

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}

	return v
}

func (v *varDCT) sigmaImage(rf *restorationFilter) ([]float32, int) {
	out := make([]float32, v.bw*v.bh)
	scale := 1 / v.quant.invGlobalScale()

	for by := range v.bh {
		for bx := range v.bw {
			pos := by*v.bw + bx

			raw := v.transform[pos]
			if raw < 128 {
				continue
			}

			t := hfTransform(raw & 127)
			cx, cy := t.blocks()

			quant := rf.epfQuantMul / (scale * float32(v.rawQuant[pos]) * epfInvSigmaNum)

			for iy := range cy {
				for ix := range cx {
					p := (by+iy)*v.bw + bx + ix
					sigma := min(quant*rf.epfSharpLUT[v.epf[p]], -1e-4)
					out[p] = 1 / sigma
				}
			}
		}
	}

	return out, v.bw
}

const (
	lfSmoothSide   = 0.20345139757231578
	lfSmoothCorner = 0.0334829185968739
	lfSmoothCenter = 1 - 4*(lfSmoothSide+lfSmoothCorner)
)

func (v *varDCT) smoothLF(factors [3]float32) {
	if v.bw <= 2 || v.bh <= 2 {
		return
	}

	invQuantLF := float32(globalScaleDenom) / (float32(v.quant.globalScale) * float32(v.quant.quantLF))

	var f [3]float32

	for c := range 3 {
		f[c] = factors[c] * invQuantLF
	}

	var out [3][]float32

	for c := range 3 {
		out[c] = make([]float32, v.bw*v.bh)
	}

	for y := range v.bh {
		for x := range v.bw {
			i := y*v.bw + x

			if y == 0 || y == v.bh-1 || x == 0 || x == v.bw-1 {
				for c := range 3 {
					out[c][i] = v.lf[c][i]
				}

				continue
			}

			var mc, sm [3]float32

			gap := float32(0.5)

			for c := range 3 {
				p := v.lf[c]
				t, m, b := i-v.bw, i, i+v.bw

				corner := p[t-1] + p[t+1] + p[b-1] + p[b+1]
				side := p[m-1] + p[m+1] + p[t] + p[b]

				mc[c] = p[m]
				sm[c] = corner*lfSmoothCorner + side*lfSmoothSide + p[m]*lfSmoothCenter

				gap = max(gap, absF((mc[c]-sm[c])/f[c]))
			}

			factor := max(3-4*gap, 0)

			for c := range 3 {
				out[c][i] = (sm[c]-mc[c])*factor + mc[c]
			}
		}
	}

	v.lf = out
}

func (v *varDCT) copyLF(src *refFrame) bool {
	if src == nil || src.w < v.bw || src.h < v.bh {
		return false
	}

	for c := range 3 {
		for y := range v.bh {
			copy(v.lf[c][y*v.bw:(y+1)*v.bw], src.planes[c][y*src.w:])
		}
	}

	return true
}
