package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// shrinkPNG box-downscales a PNG larger than size×size (keeping aspect), so a 1024 px app icon
// doesn't cost an old phone 200 KB per key. Returns the input unchanged if it can't be decoded.
func shrinkPNG(data []byte, size int) []byte {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return data
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= size && h <= size {
		return data
	}
	dw, dh := size, size
	if w > h {
		dh = max(1, h*size/w)
	} else if h > w {
		dw = max(1, w*size/h)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := b.Min.Y+y*h/dh, b.Min.Y+(y+1)*h/dh
		for x := 0; x < dw; x++ {
			x0, x1 := b.Min.X+x*w/dw, b.Min.X+(x+1)*w/dw
			// Average premultiplied colour over the source box so transparent edges don't go dark.
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			if n == 0 || a == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r * 0xff / a), G: uint8(g * 0xff / a), B: uint8(bl * 0xff / a), A: uint8(a / n >> 8),
			})
		}
	}
	var out bytes.Buffer
	if png.Encode(&out, dst) != nil {
		return data
	}
	return out.Bytes()
}
