module github.com/gen2brain/jxl

replace github.com/gen2brain/jxl/j40 => ./j40

replace github.com/gen2brain/jxl/j40c => ./j40c

go 1.19

require (
	github.com/gen2brain/jxl/j40 v0.0.0-00010101000000-000000000000
	github.com/gen2brain/jxl/j40c v0.0.0-00010101000000-000000000000
	modernc.org/libc v1.19.0
)

require (
	github.com/google/uuid v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.16 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20200410134404-eec4a21b6bb0 // indirect
	golang.org/x/sys v0.0.0-20220811171246-fbc7d0a398ab // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.4.0 // indirect
)
