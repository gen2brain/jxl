package jxl

import (
	"bytes"
	"embed"
	"image"
	"image/jpeg"
	"io/ioutil"
	"path/filepath"
	"testing"
)

//go:embed testdata/test.jxl
var testJxl []byte

//go:embed testdata/test_p.jxl
var testJxlP []byte

//go:embed testdata/test.jpg
var testJpg []byte

//go:embed testdata/oss-fuzz/*
var corpus embed.FS

func TestDecode(t *testing.T) {
	img, _, err := image.Decode(bytes.NewReader(testJxl))
	if err != nil {
		t.Fatal(err)
	}

	err = jpeg.Encode(ioutil.Discard, img, nil)
	if err != nil {
		t.Error(err)
	}
}

func TestDecodeConfig(t *testing.T) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(testJxl))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Width != 512 {
		t.Errorf("Width: got %d, want %d", cfg.Width, 512)
	}

	if cfg.Height != 512 {
		t.Errorf("Height: got %d, want %d", cfg.Height, 512)
	}
}

func TestDecodeProgressive(t *testing.T) {
	_, _, err := image.Decode(bytes.NewReader(testJxlP))
	if err == nil {
		t.Error("Progressive image decoded")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(testJxl)

	fuzzDir := "testdata/oss-fuzz"
	files, err := corpus.ReadDir(fuzzDir)
	if err != nil {
		f.Fatal(err)
	}

	for _, file := range files {
		b, _ := corpus.ReadFile(filepath.Join(fuzzDir, file.Name()))
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			t.Log(err)
			return
		}
	})
}

func BenchmarkDecodeJPEG(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, err := image.Decode(bytes.NewReader(testJpg))
		if err != nil {
			b.Error(err)
		}
	}
}

func BenchmarkDecodeJPEGXL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := Decode(bytes.NewReader(testJxl))
		if err != nil {
			b.Error(err)
		}
	}
}
