package jxl

const (
	blockDim = 8
	groupDim = 256
)

type frameType uint32

const (
	ftRegular frameType = iota
	ftLF
	ftReferenceOnly
	ftSkipProgressive
)

type frameEncoding uint32

const (
	encVarDCT frameEncoding = iota
	encModular
)

const (
	flagNoise                   = 1
	flagPatches                 = 2
	flagSplines                 = 0x10
	flagUseLFFrame              = 0x20
	flagSkipAdaptiveLFSmoothing = 0x80
)

type blendMode uint32

const (
	bmReplace blendMode = iota
	bmAdd
	bmBlend
	bmAlphaWeightedAdd
	bmMul
)

type passes struct {
	numPasses  uint32
	numDS      uint32
	shift      []uint32
	downsample []uint32
	lastPass   []uint32
}

func defaultPasses() passes { return passes{numPasses: 1} }

func (p *passes) read(r *bitReader) {
	*p = defaultPasses()

	p.numPasses = r.u32(dVal(1), dVal(2), dVal(3), dBitsOff(3, 4))
	if p.numPasses == 1 {
		return
	}

	p.numDS = r.u32(dVal(0), dVal(1), dVal(2), dBitsOff(1, 3))

	p.shift = make([]uint32, p.numPasses-1)
	for i := range p.shift {
		p.shift[i] = uint32(r.bits(2))
	}

	p.downsample = make([]uint32, p.numDS)
	for i := range p.downsample {
		p.downsample[i] = r.u32(dVal(1), dVal(2), dVal(4), dVal(8))
	}

	p.lastPass = make([]uint32, p.numDS)
	for i := range p.lastPass {
		p.lastPass[i] = r.u32(dVal(0), dVal(1), dVal(2), dBits(3))

		if p.lastPass[i] >= p.numPasses {
			r.setErr(ErrInvalid)

			return
		}
	}
}

type blendingInfo struct {
	mode         blendMode
	alphaChannel uint32
	clamp        bool
	source       uint32
}

func (b *blendingInfo) read(r *bitReader, numExtra uint32, fullFrame bool) {
	*b = blendingInfo{}

	b.mode = blendMode(r.u32(dVal(0), dVal(1), dVal(2), dBitsOff(2, 3)))
	if b.mode > bmMul {
		r.setErr(ErrInvalid)

		return
	}

	usesAlpha := b.mode == bmBlend || b.mode == bmAlphaWeightedAdd

	if numExtra > 0 && usesAlpha {
		b.alphaChannel = r.u32(dVal(0), dVal(1), dVal(2), dBitsOff(3, 3))
	}
	if (numExtra > 0 && usesAlpha) || b.mode == bmMul {
		b.clamp = r.bit()
	}
	if !(fullFrame && b.mode == bmReplace) {
		b.source = r.u32(dVal(0), dVal(1), dVal(2), dVal(3))
	}
}

type restorationFilter struct {
	gab                bool
	gabXWeight1        float32
	gabXWeight2        float32
	gabYWeight1        float32
	gabYWeight2        float32
	gabBWeight1        float32
	gabBWeight2        float32
	epfIters           uint32
	epfSharpLUT        [8]float32
	epfChannelScale    [3]float32
	epfPass1Zeroflush  float32
	epfPass2Zeroflush  float32
	epfQuantMul        float32
	epfPass0SigmaScale float32
	epfPass2SigmaScale float32
	epfBorderSadMul    float32
	epfSigmaForModular float32
}

func defaultRestorationFilter() restorationFilter {
	return restorationFilter{
		gab:         true,
		gabXWeight1: 0.115169525, gabXWeight2: 0.061248592,
		gabYWeight1: 0.115169525, gabYWeight2: 0.061248592,
		gabBWeight1: 0.115169525, gabBWeight2: 0.061248592,
		epfIters: 2,
		epfSharpLUT: [8]float32{
			0, 1.0 / 7, 2.0 / 7, 3.0 / 7, 4.0 / 7, 5.0 / 7, 6.0 / 7, 1,
		},
		epfChannelScale:    [3]float32{40, 5, 3.5},
		epfPass1Zeroflush:  0.45,
		epfPass2Zeroflush:  0.6,
		epfQuantMul:        0.46,
		epfPass0SigmaScale: 0.9,
		epfPass2SigmaScale: 6.5,
		epfBorderSadMul:    2.0 / 3.0,
		epfSigmaForModular: 1,
	}
}

func (f *restorationFilter) read(r *bitReader, enc frameEncoding) {
	*f = defaultRestorationFilter()

	if r.bit() {
		return
	}

	f.gab = r.bit()

	if f.gab && r.bit() {
		f.gabXWeight1 = r.f16()
		f.gabXWeight2 = r.f16()
		f.gabYWeight1 = r.f16()
		f.gabYWeight2 = r.f16()
		f.gabBWeight1 = r.f16()
		f.gabBWeight2 = r.f16()
	}

	f.epfIters = uint32(r.bits(2))

	if f.epfIters > 0 && enc == encVarDCT && r.bit() {
		for i := range f.epfSharpLUT {
			f.epfSharpLUT[i] = r.f16()
		}
	}
	if f.epfIters > 0 && r.bit() {
		for i := range f.epfChannelScale {
			f.epfChannelScale[i] = r.f16()
		}

		f.epfPass1Zeroflush = r.f16()
		f.epfPass2Zeroflush = r.f16()
	}
	if f.epfIters > 0 && r.bit() {
		if enc == encVarDCT {
			f.epfQuantMul = r.f16()
		}

		f.epfPass0SigmaScale = r.f16()
		f.epfPass2SigmaScale = r.f16()
		f.epfBorderSadMul = r.f16()
	}
	if f.epfIters > 0 && enc == encModular {
		f.epfSigmaForModular = r.f16()
	}

	r.extensions()
}

var (
	jpegHShift = [4]uint32{0, 1, 1, 0}
	jpegVShift = [4]uint32{0, 1, 0, 1}
)

type frameHeader struct {
	ftype           frameType
	encoding        frameEncoding
	flags           uint64
	doYCbCr         bool
	jpegUpsampling  [3]uint32
	upsampling      uint32
	ecUpsampling    []uint32
	groupSizeShift  uint32
	xQMScale        uint32
	bQMScale        uint32
	pass            passes
	lfLevel         uint32
	haveCrop        bool
	x0              int32
	y0              int32
	frameWidth      uint32
	frameHeight     uint32
	fullFrame       bool
	blending        blendingInfo
	ecBlending      []blendingInfo
	duration        uint32
	timecode        uint32
	isLast          bool
	saveAsReference uint32
	canBeReferenced bool
	saveBeforeCT    bool
	name            string
	filter          restorationFilter

	width    uint32
	height   uint32
	maxhs    uint32
	maxvs    uint32
	numExtra uint32
}

func (h *frameHeader) read(r *bitReader, m *imageMetadata, imgW, imgH uint32) {
	r.zeroPad()

	numExtra := uint32(len(m.extraChannels))

	*h = frameHeader{
		encoding:       encVarDCT,
		upsampling:     1,
		groupSizeShift: 1,
		xQMScale:       3,
		bQMScale:       2,
		pass:           defaultPasses(),
		filter:         defaultRestorationFilter(),
		numExtra:       numExtra,
	}

	h.ecUpsampling = make([]uint32, numExtra)
	for i := range h.ecUpsampling {
		h.ecUpsampling[i] = 1
	}

	h.ecBlending = make([]blendingInfo, numExtra)

	if r.bit() {
		h.isLast = true
		h.fullFrame = true
		h.finish(imgW, imgH)

		return
	}

	h.ftype = frameType(r.bits(2))
	h.encoding = frameEncoding(r.bits(1))
	h.flags = r.u64()

	if !m.xybEncoded {
		h.doYCbCr = r.bit()
	}
	if h.doYCbCr && h.flags&flagUseLFFrame == 0 {
		for i := range h.jpegUpsampling {
			h.jpegUpsampling[i] = uint32(r.bits(2))
		}
	}
	if h.flags&flagUseLFFrame == 0 {
		h.upsampling = r.u32(dVal(1), dVal(2), dVal(4), dVal(8))

		for i := range h.ecUpsampling {
			h.ecUpsampling[i] = r.u32(dVal(1), dVal(2), dVal(4), dVal(8))
		}
	}
	if h.encoding == encModular {
		h.groupSizeShift = uint32(r.bits(2))
	}
	if h.encoding == encVarDCT && m.xybEncoded {
		h.xQMScale = uint32(r.bits(3))
		h.bQMScale = uint32(r.bits(3))
	} else {
		h.xQMScale = 2
	}
	if h.ftype != ftReferenceOnly {
		h.pass.read(r)
	}
	if h.ftype == ftLF {
		h.lfLevel = r.u32(dVal(1), dVal(2), dVal(3), dVal(4))
	}
	if h.ftype != ftLF {
		h.haveCrop = r.bit()
	}

	crop := d4(dBits(8), dBitsOff(11, 256), dBitsOff(14, 2304), dBitsOff(30, 18688))

	if h.haveCrop && h.ftype != ftReferenceOnly {
		h.x0 = unpackSigned(r.u32(crop[0], crop[1], crop[2], crop[3]))
		h.y0 = unpackSigned(r.u32(crop[0], crop[1], crop[2], crop[3]))
	}
	if h.haveCrop {
		h.frameWidth = r.u32(crop[0], crop[1], crop[2], crop[3])
		h.frameHeight = r.u32(crop[0], crop[1], crop[2], crop[3])
	}

	h.deriveFullFrame(imgW, imgH)

	normal := h.ftype == ftRegular || h.ftype == ftSkipProgressive

	if normal {
		h.blending.read(r, numExtra, h.fullFrame)

		for i := range h.ecBlending {
			h.ecBlending[i].read(r, numExtra, h.fullFrame)
		}
	}
	if normal && m.haveAnimation {
		h.duration = r.u32(dVal(0), dVal(1), dBits(8), dBits(32))

		if m.anim.haveTimecodes {
			h.timecode = uint32(r.bits(32))
		}
	}

	h.isLast = h.ftype == ftRegular
	if normal {
		h.isLast = r.bit()
	}
	if h.ftype != ftLF && !h.isLast {
		h.saveAsReference = uint32(r.bits(2))
	}

	h.deriveRefs()

	saveBeforeCTDefFalse := h.canBeReferenced && h.blending.mode == bmReplace &&
		h.fullFrame && normal

	h.saveBeforeCT = h.ftype == ftLF
	if h.ftype == ftReferenceOnly || saveBeforeCTDefFalse {
		h.saveBeforeCT = r.bit()
	}

	h.name = r.str()
	h.filter.read(r, h.encoding)
	r.extensions()

	h.finish(imgW, imgH)
	h.check(r, m)
	h.postprocess(m)
}

func d4(a, b, c, d u32dist) [4]u32dist { return [4]u32dist{a, b, c, d} }

func (h *frameHeader) deriveFullFrame(imgW, imgH uint32) {
	covers := h.x0 <= 0 && h.y0 <= 0 &&
		int64(h.frameWidth)+int64(h.x0) >= int64(imgW) &&
		int64(h.frameHeight)+int64(h.y0) >= int64(imgH)

	h.fullFrame = !h.haveCrop || covers
}

func (h *frameHeader) deriveRefs() {
	h.canBeReferenced = !h.isLast && h.ftype != ftLF &&
		(h.duration == 0 || h.saveAsReference != 0)
}

func (h *frameHeader) finish(imgW, imgH uint32) {
	h.width, h.height = h.frameWidth, h.frameHeight
	if h.frameWidth == 0 {
		h.width = imgW
	}
	if h.frameHeight == 0 {
		h.height = imgH
	}

	for _, u := range h.jpegUpsampling {
		h.maxhs = max(h.maxhs, jpegHShift[u])
		h.maxvs = max(h.maxvs, jpegVShift[u])
	}
}

func (h *frameHeader) postprocess(m *imageMetadata) {
	if h.upsampling <= 1 {
		return
	}

	for i := range h.ecUpsampling {
		h.ecUpsampling[i] <<= m.extraChannels[i].dimShift
	}
}

func (h *frameHeader) check(r *bitReader, m *imageMetadata) {
	for i, ec := range m.extraChannels {
		eff := h.ecUpsampling[i] << ec.dimShift

		if h.upsampling > 1 && (eff < h.upsampling || eff > 8) {
			r.setErr(ErrInvalid)

			return
		}
		if h.flags&flagPatches != 0 && h.upsampling != 1 && h.ecUpsampling[i] != h.upsampling {
			r.setErr(ErrInvalid)

			return
		}
	}

	n := uint32(len(m.extraChannels))
	usesAlpha := func(b blendingInfo) bool {
		return b.mode == bmBlend || b.mode == bmAlphaWeightedAdd
	}

	if n > 0 && usesAlpha(h.blending) && h.blending.alphaChannel >= n {
		r.setErr(ErrInvalid)

		return
	}

	for _, b := range h.ecBlending {
		if n > 0 && usesAlpha(b) && b.alphaChannel >= n {
			r.setErr(ErrInvalid)

			return
		}
	}

	if h.flags&flagUseLFFrame != 0 && h.lfLevel >= 4 {
		r.setErr(ErrInvalid)
	}
}

func (h *frameHeader) delay(a *animation) float64 {
	if h.duration == 0 || a.tpsNumerator == 0 {
		return 0
	}

	return float64(h.duration) * float64(a.tpsDenominator) / float64(a.tpsNumerator)
}

func (h *frameHeader) logGroupDim() uint32 { return 7 + h.groupSizeShift }
func (h *frameHeader) groupDim() int       { return 1 << h.logGroupDim() }
func (h *frameHeader) lfGroupDim() int     { return h.groupDim() * blockDim }

func (h *frameHeader) sizeUpsampled() (int, int) {
	s := uint32(1) << (3 * h.lfLevel)

	return int((h.width + s - 1) / s), int((h.height + s - 1) / s)
}

func (h *frameHeader) size() (int, int) {
	w, hh := h.sizeUpsampled()
	u := int(h.upsampling)

	return (w + u - 1) / u, (hh + u - 1) / u
}

func (h *frameHeader) isVisible() bool {
	return (h.isLast || h.duration > 0) &&
		(h.ftype == ftRegular || h.ftype == ftSkipProgressive)
}

func (h *frameHeader) is444() bool {
	for c := range 3 {
		if h.maxhs != jpegHShift[h.jpegUpsampling[c]] || h.maxvs != jpegVShift[h.jpegUpsampling[c]] {
			return false
		}
	}

	return true
}

func (h *frameHeader) sizePadded() (int, int) {
	if h.encoding == encModular {
		return h.size()
	}

	w, hh := h.sizeBlocks()

	return w * blockDim, hh * blockDim
}

func (h *frameHeader) sizeBlocks() (int, int) {
	w, hh := h.size()
	bx, by := blockDim<<h.maxhs, blockDim<<h.maxvs

	return ((w + bx - 1) / bx) << h.maxhs, ((hh + by - 1) / by) << h.maxvs
}

func (h *frameHeader) sizeGroups() (int, int) {
	w, hh := h.size()
	g := h.groupDim()

	return (w + g - 1) / g, (hh + g - 1) / g
}

func (h *frameHeader) sizeLFGroups() (int, int) {
	w, hh := h.sizeBlocks()
	g := h.groupDim()

	return (w + g - 1) / g, (hh + g - 1) / g
}

func (h *frameHeader) numGroups() int {
	w, hh := h.sizeGroups()

	return w * hh
}

func (h *frameHeader) numLFGroups() int {
	w, hh := h.sizeLFGroups()

	return w * hh
}

func (h *frameHeader) numTOCEntries() int {
	n := h.numGroups()
	if n == 1 && h.pass.numPasses == 1 {
		return 1
	}

	return 2 + h.numLFGroups() + n*int(h.pass.numPasses)
}
