package j40

import (
	"math"

	"modernc.org/libc"
)

func ldexpf(tls *libc.TLS, a float32, b int32) float32 {
	return float32(math.Ldexp(float64(a), int(b)))
}

func powf(tls *libc.TLS, a, b float32) float32 {
	return float32(math.Pow(float64(a), float64(b)))
}

func hypotf(tls *libc.TLS, a, b float32) float32 {
	return float32(math.Hypot(float64(a), float64(b)))
}

func cbrtf(tls *libc.TLS, a float32) float32 {
	return float32(math.Cbrt(float64(a)))
}
