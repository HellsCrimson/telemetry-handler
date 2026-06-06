//go:build linux

package overlay

import (
	"fmt"
	"image/color"
	"strings"
	"unsafe"
)

func bytesToPixels(data []byte) []uint32 {
	if len(data) == 0 {
		return nil
	}
	return unsafe.Slice((*uint32)(unsafe.Pointer(&data[0])), len(data)/4)
}

func drawHUD(pixels []uint32, width, height int, opacity float64, hud HUD) {
	clear(pixels)

	panel := color.RGBA{R: 12, G: 16, B: 20, A: uint8(opacity * 210)}
	stroke := color.RGBA{R: 48, G: 56, B: 64, A: uint8(opacity * 255)}
	text := color.RGBA{R: 238, G: 244, B: 248, A: uint8(opacity * 255)}
	muted := color.RGBA{R: 146, G: 156, B: 164, A: uint8(opacity * 255)}
	green := color.RGBA{R: 66, G: 212, B: 119, A: uint8(opacity * 255)}
	red := color.RGBA{R: 232, G: 93, B: 93, A: uint8(opacity * 255)}
	blue := color.RGBA{R: 74, G: 163, B: 255, A: uint8(opacity * 255)}
	yellow := color.RGBA{R: 230, G: 200, B: 79, A: uint8(opacity * 255)}

	fillRect(pixels, width, height, 0, 0, width, height, panel)
	drawRect(pixels, width, height, 0, 0, width, height, stroke)

	status := "LIVE"
	statusColor := green
	if !hud.Connected {
		status = "NO DATA"
		statusColor = red
	} else if hud.Stale {
		status = "STALE"
		statusColor = yellow
	}
	drawText(pixels, width, height, 12, 12, 2, status, statusColor)

	drawText(pixels, width, height, 12, 38, 4, hud.SpeedKPH, text)
	drawText(pixels, width, height, 14+len(hud.SpeedKPH)*24, 58, 1, "KM/H", muted)

	gear := "G" + hud.Gear
	drawText(pixels, width, height, width-12-len(gear)*12, 14, 2, gear, text)

	rpmLine := fmt.Sprintf("%s/%s RPM", hud.RPM, hud.MaxRPM)
	drawText(pixels, width, height, 12, 92, 1, rpmLine, muted)
	drawBar(pixels, width, height, 12, 108, width-24, 10, hud.RPMRatio, yellow, stroke)

	barY := 130
	barW := (width - 40) / 3
	drawLabeledBar(pixels, width, height, 12, barY, barW, "T", hud.Throttle, green, stroke, muted)
	drawLabeledBar(pixels, width, height, 20+barW, barY, barW, "B", hud.Brake, red, stroke, muted)
	drawLabeledBar(pixels, width, height, 28+barW*2, barY, barW, "C", hud.Clutch, blue, stroke, muted)
}

func drawLabeledBar(pixels []uint32, width, height, x, y, w int, label string, value float64, fill color.RGBA, bg color.RGBA, text color.RGBA) {
	drawText(pixels, width, height, x, y, 1, label, text)
	drawBar(pixels, width, height, x+12, y+1, w-12, 8, value, fill, bg)
}

func drawBar(pixels []uint32, width, height, x, y, w, h int, value float64, fill color.RGBA, bg color.RGBA) {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	fillRect(pixels, width, height, x, y, w, h, color.RGBA{R: 28, G: 34, B: 39, A: bg.A})
	fillRect(pixels, width, height, x, y, int(float64(w)*value), h, fill)
	drawRect(pixels, width, height, x, y, w, h, bg)
}

func clear(pixels []uint32) {
	for i := range pixels {
		pixels[i] = 0
	}
}

func fillRect(pixels []uint32, width, height, x, y, w, h int, c color.RGBA) {
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > width {
		w = width - x
	}
	if y+h > height {
		h = height - y
	}
	if w <= 0 || h <= 0 {
		return
	}
	packed := packPremul(c)
	for yy := y; yy < y+h; yy++ {
		row := yy * width
		for xx := x; xx < x+w; xx++ {
			pixels[row+xx] = packed
		}
	}
}

func drawRect(pixels []uint32, width, height, x, y, w, h int, c color.RGBA) {
	fillRect(pixels, width, height, x, y, w, 1, c)
	fillRect(pixels, width, height, x, y+h-1, w, 1, c)
	fillRect(pixels, width, height, x, y, 1, h, c)
	fillRect(pixels, width, height, x+w-1, y, 1, h, c)
}

func drawText(pixels []uint32, width, height, x, y, scale int, text string, c color.RGBA) {
	cursor := x
	for _, r := range strings.ToUpper(text) {
		if r == ' ' {
			cursor += 4 * scale
			continue
		}
		glyph, ok := font[r]
		if !ok {
			glyph = font['?']
		}
		for row, bits := range glyph {
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) != 0 {
					fillRect(pixels, width, height, cursor+col*scale, y+row*scale, scale, scale, c)
				}
			}
		}
		cursor += 6 * scale
	}
}

func packPremul(c color.RGBA) uint32 {
	a := uint32(c.A)
	r := uint32(c.R) * a / 255
	g := uint32(c.G) * a / 255
	b := uint32(c.B) * a / 255
	return a<<24 | r<<16 | g<<8 | b
}

var font = map[rune][7]byte{
	'0': {0x0e, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0e},
	'1': {0x04, 0x0c, 0x04, 0x04, 0x04, 0x04, 0x0e},
	'2': {0x0e, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1f},
	'3': {0x1e, 0x01, 0x01, 0x0e, 0x01, 0x01, 0x1e},
	'4': {0x02, 0x06, 0x0a, 0x12, 0x1f, 0x02, 0x02},
	'5': {0x1f, 0x10, 0x10, 0x1e, 0x01, 0x01, 0x1e},
	'6': {0x06, 0x08, 0x10, 0x1e, 0x11, 0x11, 0x0e},
	'7': {0x1f, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0e, 0x11, 0x11, 0x0e, 0x11, 0x11, 0x0e},
	'9': {0x0e, 0x11, 0x11, 0x0f, 0x01, 0x02, 0x0c},
	'/': {0x01, 0x01, 0x02, 0x04, 0x08, 0x10, 0x10},
	'-': {0x00, 0x00, 0x00, 0x1f, 0x00, 0x00, 0x00},
	'A': {0x0e, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11},
	'B': {0x1e, 0x11, 0x11, 0x1e, 0x11, 0x11, 0x1e},
	'C': {0x0e, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0e},
	'D': {0x1e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1e},
	'E': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x1f},
	'F': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x10},
	'G': {0x0e, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0e},
	'H': {0x11, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11},
	'I': {0x0e, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0e},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1f},
	'M': {0x11, 0x1b, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0e},
	'P': {0x1e, 0x11, 0x11, 0x1e, 0x10, 0x10, 0x10},
	'R': {0x1e, 0x11, 0x11, 0x1e, 0x14, 0x12, 0x11},
	'S': {0x0f, 0x10, 0x10, 0x0e, 0x01, 0x01, 0x1e},
	'T': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0a, 0x04},
	'Y': {0x11, 0x11, 0x0a, 0x04, 0x04, 0x04, 0x04},
	'?': {0x0e, 0x11, 0x01, 0x02, 0x04, 0x00, 0x04},
}
