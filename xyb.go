package jxl

import "math"

type xybParams struct {
	mat            [9]float32
	biasCbrt       [3]float32
	scaledBias     [3]float32
	intensityScale float32
}

func newXYBParams(o *opsinInverseMatrix, intensityTarget float32) xybParams {
	p := xybParams{mat: o.inverse, intensityScale: 255 / intensityTarget}

	for i := range 3 {
		p.biasCbrt[i] = float32(math.Cbrt(float64(o.biases[i])))
		p.scaledBias[i] = o.biases[i] * p.intensityScale
	}

	return p
}

func (p *xybParams) apply(x, y, b float32) (float32, float32, float32) {
	l := y + x - p.biasCbrt[0]
	m := y - x - p.biasCbrt[1]
	s := b - p.biasCbrt[2]

	l = l*l*(l*p.intensityScale) + p.scaledBias[0]
	m = m*m*(m*p.intensityScale) + p.scaledBias[1]
	s = s*s*(s*p.intensityScale) + p.scaledBias[2]

	return p.mat[0]*l + p.mat[1]*m + p.mat[2]*s,
		p.mat[3]*l + p.mat[4]*m + p.mat[5]*s,
		p.mat[6]*l + p.mat[7]*m + p.mat[8]*s
}

var (
	srgbP = [5]float32{
		-5.135152395e-4, 5.287254571e-3, 3.903842876e-1, 1.474205315, 7.352629620e-1,
	}
	srgbQ = [5]float32{
		1.004519624e-2, 3.036675394e-1, 1.340816930, 9.258482155e-1, 2.424867759e-2,
	}
)

func (p *xybParams) srgbValues() [29]float32 {
	var v [29]float32

	for i := range 3 {
		v[i] = p.biasCbrt[i]
		v[4+i] = p.scaledBias[i]
	}

	v[3] = p.intensityScale

	for i := range 9 {
		v[7+i] = p.mat[i]
	}

	for i := range 5 {
		v[16+i] = srgbP[i]
		v[21+i] = srgbQ[i]
	}

	v[26] = 12.92
	v[27] = 0.0031308
	v[28] = math.Float32frombits(0x7fffffff)

	return v
}

func (p *xybParams) srgbConsts() *[29][8]float32 {
	k := new([29][8]float32)

	for i, v := range p.srgbValues() {
		for j := range k[i] {
			k[i][j] = v
		}
	}

	return k
}

func (p *xybParams) srgbConsts512() *[29][16]float32 {
	k := new([29][16]float32)

	for i, v := range p.srgbValues() {
		for j := range k[i] {
			k[i][j] = v
		}
	}

	return k
}

func evalRationalPoly(x float32, p, q *[5]float32) float32 {
	yp := p[4]
	yq := q[4]

	for i := 3; i >= 0; i-- {
		yp = yp*x + p[i]
		yq = yq*x + q[i]
	}

	return yp / yq
}

func linearToSRGB(v float32) float32 {
	sign := float32(1)
	if v < 0 {
		sign, v = -1, -v
	}

	if v < 0.0031308 {
		return sign * v * 12.92
	}

	return sign * evalRationalPoly(float32(math.Sqrt(float64(v))), &srgbP, &srgbQ)
}

func linearToGamma(v, gamma float32) float32 {
	if v <= 1e-5 {
		return 0
	}

	return float32(math.Pow(float64(v), float64(gamma)))
}

var (
	pqP   = [5]float32{1.351392e-02, -1.095778e+00, 5.522776e+01, 1.492516e+02, 4.838434e+01}
	pqQ   = [5]float32{1.012416e+00, 2.016708e+01, 9.263710e+01, 1.120607e+02, 2.590418e+01}
	pqPLo = [5]float32{9.863406e-06, 3.881234e-01, 1.352821e+02, 6.889862e+04, -2.864824e+05}
	pqQLo = [5]float32{3.371868e+01, 1.477719e+03, 1.608477e+04, -4.389884e+04, -2.072546e+05}
)

func linearToPQ(v, scale float32) float32 {
	a := float32(math.Abs(float64(v)))
	x := float32(math.Sqrt(math.Sqrt(float64(a * scale))))

	var e float32
	if a < 1e-4 {
		e = evalRationalPoly(x, &pqPLo, &pqQLo)
	} else {
		e = evalRationalPoly(x, &pqP, &pqQ)
	}

	return float32(math.Copysign(float64(e), float64(v)))
}

const (
	hlgA = 0.17883277
	hlgB = 1 - 4*hlgA
	hlgC = 0.5599107295
)

func linearToHLG(v float32) float32 {
	a := math.Abs(float64(v))

	var e float64
	if a <= 1.0/12 {
		e = math.Sqrt(3 * a)
	} else {
		e = hlgA*math.Log(12*a-hlgB) + hlgC
	}

	return float32(math.Copysign(e, float64(v)))
}

type xybOutput struct {
	tf        customTF
	pqScale   float32
	ootf      bool
	ootfExp   float32
	luminance [3]float32
}

func (o *xybOutput) toScene(r, g, b float32) (float32, float32, float32) {
	if !o.ootf {
		return r, g, b
	}

	l := o.luminance[0]*r + o.luminance[1]*g + o.luminance[2]*b
	ratio := min(float32(math.Pow(float64(l), float64(o.ootfExp))), 1e9)

	return r * ratio, g * ratio, b * ratio
}

func (o *xybOutput) encode(v float32) float32 {
	if o.tf.haveGamma {
		return linearToGamma(v, o.tf.gammaValue())
	}

	switch o.tf.tf {
	case tfSRGB:
		return linearToSRGB(v)
	case tfBT709:
		if v <= 0.018 {
			return v * 4.5
		}

		return 1.099*float32(math.Pow(float64(v), 0.45)) - 0.099
	case tfDCI:
		return linearToGamma(v, 1.0/2.6)
	case tfPQ:
		return linearToPQ(v, o.pqScale)
	case tfHLG:
		return linearToHLG(v)
	}

	return v
}

type mat3 [3][3]float32

func (a mat3) mul(b mat3) mat3 {
	var c mat3

	for x := range 3 {
		t := [3]float64{float64(b[0][x]), float64(b[1][x]), float64(b[2][x])}

		for y := range 3 {
			c[y][x] = float32(float64(a[y][0])*t[0] + float64(a[y][1])*t[1] + float64(a[y][2])*t[2])
		}
	}

	return c
}

func (a mat3) vec(b [3]float32) [3]float32 {
	var c [3]float32

	for y := range 3 {
		var e float64
		for x := range 3 {
			e += float64(a[y][x]) * float64(b[x])
		}

		c[y] = float32(e)
	}

	return c
}

func (a mat3) inverse() (mat3, bool) {
	m := func(y, x int) float64 { return float64(a[y][x]) }

	t := [3][3]float64{
		{m(1, 1)*m(2, 2) - m(1, 2)*m(2, 1), m(0, 2)*m(2, 1) - m(0, 1)*m(2, 2), m(0, 1)*m(1, 2) - m(0, 2)*m(1, 1)},
		{m(1, 2)*m(2, 0) - m(1, 0)*m(2, 2), m(0, 0)*m(2, 2) - m(0, 2)*m(2, 0), m(0, 2)*m(1, 0) - m(0, 0)*m(1, 2)},
		{m(1, 0)*m(2, 1) - m(1, 1)*m(2, 0), m(0, 1)*m(2, 0) - m(0, 0)*m(2, 1), m(0, 0)*m(1, 1) - m(0, 1)*m(1, 0)},
	}

	det := m(0, 0)*t[0][0] + m(0, 1)*t[1][0] + m(0, 2)*t[2][0]
	if math.Abs(det) < 1e-10 {
		return mat3{}, false
	}

	var out mat3

	idet := 1 / det
	for y := range 3 {
		for x := range 3 {
			out[y][x] = float32(t[y][x] * idet)
		}
	}

	return out, true
}

func diag3(v [3]float32) mat3 {
	return mat3{{v[0], 0, 0}, {0, v[1], 0}, {0, 0, v[2]}}
}

func finite32(v float32) bool {
	return !math.IsInf(float64(v), 0) && !math.IsNaN(float64(v))
}

func whiteVector(w [2]float32) ([3]float32, bool) {
	if w[0] < 0 || w[0] > 1 || w[1] <= 0 || w[1] > 1 {
		return [3]float32{}, false
	}

	v := [3]float32{w[0] / w[1], 1, (1 - w[0] - w[1]) / w[1]}

	return v, finite32(v[0]) && finite32(v[2])
}

func primariesToXYZ(p [3][2]float32, w [2]float32) (mat3, bool) {
	wv, ok := whiteVector(w)
	if !ok {
		return mat3{}, false
	}

	prim := mat3{
		{p[0][0], p[1][0], p[2][0]},
		{p[0][1], p[1][1], p[2][1]},
		{1 - p[0][0] - p[0][1], 1 - p[1][0] - p[1][1], 1 - p[2][0] - p[2][1]},
	}

	inv, ok := prim.inverse()
	if !ok {
		return mat3{}, false
	}

	return prim.mul(diag3(inv.vec(wv))), true
}

var (
	bradford    = mat3{{0.8951, 0.2664, -0.1614}, {-0.7502, 1.7135, 0.0367}, {0.0389, -0.0685, 1.0296}}
	bradfordInv = mat3{{0.9869929, -0.1470543, 0.1599627}, {0.4323053, 0.5183603, 0.0492912}, {-0.0085287, 0.0400428, 0.9684867}}
)

func adaptToXYZD50(w [2]float32) (mat3, bool) {
	wv, ok := whiteVector(w)
	if !ok {
		return mat3{}, false
	}

	lms := bradford.vec(wv)
	lms50 := bradford.vec([3]float32{0.96422, 1, 0.82521})

	var s [3]float32

	for i := range 3 {
		if lms[i] == 0 {
			return mat3{}, false
		}

		if s[i] = lms50[i] / lms[i]; !finite32(s[i]) {
			return mat3{}, false
		}
	}

	return bradfordInv.mul(diag3(s).mul(bradford)), true
}

var (
	whiteD65      = [2]float32{0.3127, 0.3290}
	srgbXY        = [3][2]float32{{0.639998686, 0.330010138}, {0.300003784, 0.600003357}, {0.150002046, 0.059997204}}
	srgbLuminance = [3]float32{0.2126, 0.7152, 0.0722}
)

func (c customXY) value() [2]float32 {
	return [2]float32{float32(float64(c.x) / 1e6), float32(float64(c.y) / 1e6)}
}

func (c *colorEncoding) whitePointXY() ([2]float32, bool) {
	switch c.white {
	case wpD65:
		return whiteD65, true
	case wpDCI:
		return [2]float32{0.314, 0.351}, true
	case wpE:
		return [2]float32{1.0 / 3, 1.0 / 3}, true
	case wpCustom:
		return c.whiteXY.value(), true
	}

	return [2]float32{}, false
}

func (c *colorEncoding) primariesXY() ([3][2]float32, bool) {
	switch c.prim {
	case prSRGB:
		return srgbXY, true
	case prBT2100:
		return [3][2]float32{{0.708, 0.292}, {0.170, 0.797}, {0.131, 0.046}}, true
	case prP3:
		return [3][2]float32{{0.680, 0.320}, {0.265, 0.690}, {0.150, 0.060}}, true
	case prCustom:
		return [3][2]float32{c.customPrim[0].value(), c.customPrim[1].value(), c.customPrim[2].value()}, true
	}

	return [3][2]float32{}, false
}

func xybOutputFor(c *colorEncoding, inverse [9]float32, intensityTarget float32) ([9]float32, xybOutput, bool) {
	inv := mat3{
		{inverse[0], inverse[1], inverse[2]},
		{inverse[3], inverse[4], inverse[5]},
		{inverse[6], inverse[7], inverse[8]},
	}

	gray := c.space == csGray
	out := xybOutput{tf: customTF{tf: tfLinear}, luminance: srgbLuminance}

	if !c.wantICC && !(gray && c.white != wpD65) {
		out.tf = c.tf

		if !gray && (c.prim != prSRGB || c.white != wpD65) {
			p, okP := c.primariesXY()
			w, okW := c.whitePointXY()
			if !okP || !okW {
				return inverse, out, false
			}

			srgbToXYZ, ok1 := primariesToXYZ(srgbXY, whiteD65)
			d65ToD50, ok2 := adaptToXYZD50(whiteD65)
			toXYZ, ok3 := primariesToXYZ(p, w)
			toD50, ok4 := adaptToXYZD50(w)
			fromXYZD50, ok5 := toD50.mul(toXYZ).inverse()
			if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
				return inverse, out, false
			}

			out.luminance = toXYZ[1]
			inv = fromXYZD50.mul(d65ToD50.mul(srgbToXYZ)).mul(inv)
		}
	}

	if gray {
		l := out.luminance
		inv = mat3{l, l, l}.mul(inv)
	}

	if !out.tf.haveGamma {
		switch out.tf.tf {
		case tfPQ:
			out.pqScale = intensityTarget * (1.0 / 10000)
		case tfHLG:
			gamma := (1 / float32(1.2)) * float32(math.Pow(1.111, -math.Log2(float64(intensityTarget/1000))))
			out.ootfExp = gamma - 1
			out.ootf = out.ootfExp < -0.01 || out.ootfExp > 0.01
		}
	}

	return [9]float32{
		inv[0][0], inv[0][1], inv[0][2],
		inv[1][0], inv[1][1], inv[1][2],
		inv[2][0], inv[2][1], inv[2][2],
	}, out, true
}
