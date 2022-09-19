package jxl

import (
	"bytes"
	_ "embed"
	"image"
	"image/jpeg"
	"io/ioutil"
	"testing"
)

//go:embed testdata/test.jxl
var testJxl []byte

//go:embed testdata/test.jpg
var testJpg []byte

func TestDecode(t *testing.T) {
	r := bytes.NewReader(testJxl)

	img, _, err := image.Decode(r)
	if err != nil {
		t.Fatal(err)
	}

	err = jpeg.Encode(ioutil.Discard, img, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDecodeConfig(t *testing.T) {
	r := bytes.NewReader(testJxl)

	_, _, err := image.DecodeConfig(r)
	if err != nil {
		t.Fatal(err)
	}
}

func BenchmarkDecodeJPEG(b *testing.B) {
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(testJpg)
		_, _, err := image.Decode(r)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeJPEGXL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(testJxl)
		_, err := Decode(r)
		if err != nil {
			b.Fatal(err)
		}
	}
}
