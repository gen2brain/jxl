package jxl

var lfQuantDefault = [3]float32{1.0 / 4096, 1.0 / 512, 1.0 / 256}

type lfQuantFactors struct {
	factors [3]float32
}

func (l *lfQuantFactors) read(r *bitReader) {
	if r.bit() {
		l.factors = lfQuantDefault

		return
	}

	for i := range l.factors {
		l.factors[i] = r.f16() / 128

		if l.factors[i] < 1e-8 {
			r.setErr(ErrInvalid)

			return
		}
	}
}

type frame struct {
	hdr     frameHeader
	toc     toc
	modular modImage
	lfQuant lfQuantFactors
	vardct  *varDCT
	patches patchDict
	splines splines
	noise   noiseParams

	tree       *maTree
	learned    *maTree
	gh         groupHeader
	beginC     int
	predictor  uint8
	predictors []uint8
	threads    int
	workers    *[]worker
}

type rect struct {
	x0, y0, w, h int
}

func (d *decoder) readFrameHeader(r *bitReader) (*frame, error) {
	f := &frame{threads: d.threads, workers: &d.workers}

	w, h := d.hdr.size.xsize, d.hdr.size.ysize
	if d.preview {
		w, h = d.hdr.meta.preview.xsize, d.hdr.meta.preview.ysize
	}

	f.hdr.read(r, &d.hdr.meta, w, h)

	if err := r.err(); err != nil {
		return nil, err
	}

	if err := d.checkFrameSize(f.hdr.width, f.hdr.height); err != nil {
		return nil, err
	}

	f.toc.read(r, f.hdr.numTOCEntries())

	if err := r.err(); err != nil {
		return nil, err
	}

	for _, e := range f.hdr.ecUpsampling {
		if e != f.hdr.upsampling {
			return nil, ErrUnsupported
		}
	}

	return f, nil
}

func (d *decoder) decodeFrame(f *frame, r *bitReader) error {
	sections, err := f.split(r)
	if err != nil {
		return err
	}

	if err := d.decodeLFGlobal(f, sections[0]); err != nil {
		return err
	}

	if err := f.decodeGroups(sections); err != nil {
		return err
	}

	if len(f.modular.channels) > 0 {
		f.modular.inverse(sections[0], f.gh.transforms, &f.gh.wp)

		if err := sections[0].err(); err != nil {
			return err
		}
	}

	return nil
}

func (f *frame) split(r *bitReader) ([]*bitReader, error) {
	sections := make([]*bitReader, len(f.toc.entries))

	for i, n := range f.toc.entries {
		sections[i] = r.split(int(n))

		if err := r.err(); err != nil {
			return nil, err
		}
	}

	if f.toc.permutation == nil {
		return sections, nil
	}

	if len(f.toc.permutation) != len(sections) {
		return nil, ErrInvalid
	}

	shuffled := make([]*bitReader, len(sections))
	for i, p := range f.toc.permutation {
		shuffled[i] = sections[p]
	}

	return shuffled, nil
}

func (d *decoder) decodeLFGlobal(f *frame, global *bitReader) error {
	m := &d.hdr.meta

	if f.hdr.flags&flagPatches != 0 {
		w, h := f.hdr.sizePadded()

		f.patches.read(global, w, h, len(m.extraChannels), &d.preCT)

		if err := global.err(); err != nil {
			return err
		}
	}

	if f.hdr.flags&flagSplines != 0 {
		f.splines.read(global, f.hdr.width*f.hdr.height)

		if err := global.err(); err != nil {
			return err
		}
	}

	if f.hdr.flags&flagNoise != 0 {
		f.noise.read(global)
	}

	f.lfQuant.read(global)

	if f.hdr.encoding == encVarDCT {
		f.vardct = newVarDCT(f, d.hdr.transform.opsin.quant, &d.arena)

		if f.hdr.flags&flagUseLFFrame != 0 && !f.vardct.copyLF(d.lfFrames[f.hdr.lfLevel]) {
			return ErrInvalid
		}

		f.vardct.quant.read(global)
		f.vardct.bctx.read(global)
		f.vardct.cmap.read(global)

		if err := global.err(); err != nil {
			return err
		}
	}

	if f.hdr.flags&flagSplines != 0 {
		cmap := colorCorrelation{factor: defaultColorFactor, baseB: 1}
		if f.vardct != nil {
			cmap = f.vardct.cmap
		}

		cw, ch := f.hdr.size()

		if !f.splines.initDrawCache(cw, ch, &cmap) {
			return ErrInvalid
		}
	}

	if global.bit() {
		limit := 1024 + int(f.hdr.width)*int(f.hdr.height)*
			(m.colorChannels()+len(m.extraChannels))/16

		f.tree = &maTree{}
		f.tree.read(global, min(limit, 1<<22))

		if err := global.err(); err != nil {
			return err
		}
	}

	f.modular.channels = d.modularChannels(&f.hdr)
	f.modular.bitDepth = m.depth.bitsPerSample
	f.modular.threads = f.threads

	if len(f.modular.channels) == 0 {
		if f.vardct == nil {
			return ErrUnsupported
		}

		return nil
	}

	f.gh.read(global)

	if err := global.err(); err != nil {
		return err
	}

	f.modular.metaApply(global, f.gh.transforms)

	if err := global.err(); err != nil {
		return err
	}

	f.beginC = f.modular.firstGroupedChannel(f.hdr.groupDim())

	f.modular.decodeCoded(global, &f.gh, f.tree, 0, f.beginC)

	return global.err()
}

func (m *modImage) firstGroupedChannel(groupDim int) int {
	c := m.nbMeta

	for ; c < len(m.channels); c++ {
		if m.channels[c].w > groupDim || m.channels[c].h > groupDim {
			break
		}
	}

	return c
}

func (f *frame) decodeGroups(sections []*bitReader) error {
	numLF := f.hdr.numLFGroups()
	numGroups := f.hdr.numGroups()
	passes := int(f.hdr.pass.numPasses)

	single := len(sections) == 1

	if !single && len(sections) != 2+numLF+numGroups*passes {
		return ErrInvalid
	}

	at := func(i int) *bitReader {
		if single {
			return sections[0]
		}

		return sections[i]
	}

	nw := 1
	if !single {
		nw = min(f.threads, max(numLF, numGroups))
	}

	workers := f.takeWorkers(nw)

	lfDim := f.hdr.lfGroupDim()
	lfx, _ := f.hdr.sizeLFGroups()

	useLF := f.hdr.flags&flagUseLFFrame != 0

	err := overTasks(nw, numLF, func(w, g int) error {
		br := at(1 + g)

		if f.vardct != nil && !useLF {
			f.decodeVarDCTLF(br, g, f.tree)

			if err := br.err(); err != nil {
				return err
			}
		}

		if err := f.decodeGroup(&workers[w], br, f.groupRect(g%lfx, g/lfx, lfDim),
			3, 1<<30, 1+numLF+g); err != nil {
			return err
		}

		if f.vardct != nil {
			f.decodeHFMetadata(br, g, f.tree)

			if err := br.err(); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	if f.vardct != nil {
		if f.hdr.flags&flagSkipAdaptiveLFSmoothing == 0 && !useLF {
			f.vardct.smoothLF(f.lfQuant.factors)
		}

		br := at(1 + numLF)

		f.decodeHFGlobal(br, f.tree)

		if err := br.err(); err != nil {
			return err
		}
	}

	dim := f.hdr.groupDim()
	gx, _ := f.hdr.sizeGroups()

	for i := range min(nw, numGroups) {
		workers[i].alloc(f, passes)
	}

	return overTasks(nw, numGroups, func(w, g int) error {
		readers := workers[w].readers

		for pass := range passes {
			readers[pass] = at(2 + numLF + pass*numGroups + g)
		}

		if f.vardct != nil {
			if err := f.decodeVarDCTGroup(&workers[w], g, readers); err != nil {
				return err
			}
		}

		for pass := range passes {
			minShift, maxShift := f.hdr.pass.bracket(pass)
			id := 1 + numLF*3 + numQuantTables + numGroups*pass + g

			if err := f.decodeGroup(&workers[w], readers[pass],
				f.groupRect(g%gx, g/gx, dim), minShift, maxShift, id); err != nil {
				return err
			}
		}

		return nil
	})
}

func (f *frame) groupRect(gx, gy, dim int) rect {
	return rect{gx * dim, gy * dim, dim, dim}
}

type worker struct {
	group   groupScratch
	dct     dctScratch
	readers []*bitReader
}

func (f *frame) takeWorkers(n int) []worker {
	if f.workers == nil {
		return make([]worker, n)
	}

	if len(*f.workers) < n {
		*f.workers = append(*f.workers, make([]worker, n-len(*f.workers))...)
	}

	return (*f.workers)[:n]
}

func (w *worker) alloc(f *frame, passes int) {
	if len(w.readers) != passes {
		w.readers = make([]*bitReader, passes)
	}

	if f.vardct != nil {
		w.dct.alloc(f)
	}
}

type groupScratch struct {
	sub   modImage
	chans []modChannel
	data  []int32
	used  int
	nchan int
	crops []rect
	dest  []*modChannel
}

func (g *groupScratch) reset() {
	g.sub = modImage{arena: g}
	g.sub.channels = g.sub.channels[:0]
	g.crops = g.crops[:0]
	g.dest = g.dest[:0]
	g.used = 0
	g.nchan = 0
}

func (g *groupScratch) channel(w, h, hshift, vshift int) *modChannel {
	if g.nchan == len(g.chans) {
		g.chans = append(g.chans, modChannel{})
	}

	if g.used+w*h > len(g.data) {
		g.data = append(g.data, make([]int32, g.used+w*h-len(g.data))...)
	}

	data := g.data[g.used : g.used+w*h : g.used+w*h]
	g.used += w * h

	clear(data)

	c := &g.chans[g.nchan]
	g.nchan++

	*c = modChannel{w: w, h: h, hshift: hshift, vshift: vshift, data: data}

	return c
}

func (g *groupScratch) add(w, h, hshift, vshift int) {
	g.sub.channels = append(g.sub.channels, g.channel(w, h, hshift, vshift))
}

func (f *frame) decodeGroup(w *worker, r *bitReader, rc rect, minShift, maxShift, streamID int) error {
	g := &w.group

	g.reset()

	sub := &g.sub
	sub.bitDepth = f.modular.bitDepth

	for c := f.beginC; c < len(f.modular.channels); c++ {
		fc := f.modular.channels[c]

		if shift := min(fc.hshift, fc.vshift); shift > maxShift || shift < minShift {
			continue
		}

		cr := rect{rc.x0 >> fc.hshift, rc.y0 >> fc.vshift,
			rc.w >> fc.hshift, rc.h >> fc.vshift}

		cr.w = min(cr.w, fc.w-cr.x0)
		cr.h = min(cr.h, fc.h-cr.y0)

		if cr.w <= 0 || cr.h <= 0 {
			continue
		}

		g.add(cr.w, cr.h, fc.hshift, fc.vshift)

		g.crops = append(g.crops, cr)
		g.dest = append(g.dest, fc)
	}

	if len(sub.channels) == 0 {
		return nil
	}

	sub.decodeStream(r, nil, f.tree, streamID)

	if err := r.err(); err != nil {
		return err
	}

	for i, src := range sub.channels {
		cr, fc := g.crops[i], g.dest[i]

		for y := range cr.h {
			copy(fc.row(cr.y0 + y)[cr.x0:cr.x0+cr.w], src.row(y))
		}
	}

	return nil
}

func (p *passes) bracket(pass int) (int, int) {
	minShift, maxShift := 3, 2

	for i := 0; i <= pass; i++ {
		for j := range int(p.numDS) {
			if uint32(i) == p.lastPass[j] {
				minShift = ceilLog2(p.downsample[j])
			}
		}
		if i+1 == int(p.numPasses) {
			minShift = 0
		}
		if i != pass {
			maxShift = max(minShift-1, 0)
		}
	}

	return minShift, maxShift
}

func (d *decoder) modularChannels(h *frameHeader) []*modChannel {
	var out []*modChannel

	w, hh := h.size()
	colorChannels := d.hdr.meta.colorChannels()

	switch {
	case h.encoding == encVarDCT:
		colorChannels = 0
	case d.hdr.meta.xybEncoded:
		colorChannels = 3
	}

	for c := range colorChannels {
		hs, vs := 0, 0

		if c < 3 {
			hs = int(h.maxhs - jpegHShift[h.jpegUpsampling[c]])
			vs = int(h.maxvs - jpegVShift[h.jpegUpsampling[c]])
		}

		out = append(out, newModChannel(divCeil(w, 1<<hs), divCeil(hh, 1<<vs), hs, vs))
	}

	uw, uh := h.sizeUpsampled()
	shiftColor := ceilLog2(h.upsampling)

	for _, ecups := range h.ecUpsampling {
		shift := ceilLog2(ecups) - shiftColor
		if shift < 0 {
			return nil
		}

		out = append(out, newModChannel(
			divCeil(uw, int(ecups)), divCeil(uh, int(ecups)), shift, shift))
	}

	return out
}
