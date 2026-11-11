package jxl

import (
	"bytes"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestXYBForwardInvertsApply(t *testing.T) {
	rng := rand.New(rand.NewPCG(97, 101))

	o := defaultOpsinInverseMatrix()

	for _, target := range []float32{255, 100, 1000} {
		p := newXYBParams(&o, target)

		fwd, ok := newXYBForward(&p)
		if !ok {
			t.Fatalf("intensity %g: the matrix was not invertible", target)
		}

		var worst float64

		for range 20000 {
			r := float32(rng.Float64())
			g := float32(rng.Float64())
			b := float32(rng.Float64())

			x, y, bb := fwd.apply(r, g, b)

			gr, gg, gb := p.apply(x, y, bb)

			for i, d := range [3]float64{
				math.Abs(float64(gr - r)), math.Abs(float64(gg - g)), math.Abs(float64(gb - b)),
			} {
				if math.IsNaN(d) {
					t.Fatalf("intensity %g channel %d: %g %g %g gave NaN", target, i, r, g, b)
				}

				worst = math.Max(worst, d)
			}
		}

		if worst > 2e-5 {
			t.Fatalf("intensity %g: round trip is %g off", target, worst)
		}

		t.Logf("intensity %g: round trip within %g", target, worst)
	}
}

func TestSRGBToLinearInverts(t *testing.T) {
	var worst float64

	for i := range 65536 {
		v := float32(i) / 65535

		if d := math.Abs(float64(linearToSRGB(srgbToLinear(v)) - v)); d > worst {
			worst = d
		}
	}

	if worst > 1e-4 {
		t.Fatalf("round trip is %g off", worst)
	}

	t.Logf("sRGB round trip within %g", worst)
}

func TestXYBOutputTransfer(t *testing.T) {
	for _, tc := range []struct {
		name string
		want transferFunction
	}{
		{"xyb_gray_dci.jxl", tfLinear},
		{"xyb_gray_pq.jxl", tfPQ},
		{"xyb_pq.jxl", tfPQ},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", "streams", tc.name))
		if err != nil {
			t.Skip(err)
		}

		_, info, err := DecodeColor(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if info.Transfer != uint16(tc.want) {
			t.Fatalf("%s: transfer %d, want %d", tc.name, info.Transfer, tc.want)
		}
	}
}

func TestCbrt32MatchesMathCbrt(t *testing.T) {
	const stride = 16

	var (
		wg  sync.WaitGroup
		bad [16]uint64
	)

	for w := range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			lo := uint64(w) << 28

			for u := lo; u < lo+1<<28; u += stride {
				x := math.Float32frombits(uint32(u))
				want := float32(math.Cbrt(float64(x)))
				got := cbrt32(x)

				if got != want && !(got != got && want != want) {
					bad[w]++
				}
			}
		}()
	}

	wg.Wait()

	total := uint64(0)
	for _, n := range bad {
		total += n
	}

	if total != 0 {
		t.Fatalf("%d of 2^32 float32 inputs differ from math.Cbrt", total)
	}

	t.Logf("every %d-th float32 agrees with math.Cbrt", uint64(stride))
}
