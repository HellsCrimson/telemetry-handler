package overlay

import (
	"fmt"
	"image/color"
	"math"
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
	amber := color.RGBA{R: 240, G: 180, B: 60, A: uint8(opacity * 255)}
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

	drawText(pixels, width, height, width-12-len(hud.Gear)*12, 14, 2, hud.Gear, text)

	rpmLine := fmt.Sprintf("%s/%s RPM", hud.RPM, hud.MaxRPM)
	drawText(pixels, width, height, 12, 92, 1, rpmLine, muted)
	drawBar(pixels, width, height, 12, 108, width-24, 10, hud.RPMRatio, yellow, stroke)

	// Throttle/brake history graph on the left, clutch/handbrake tell-tales on
	// the right (illuminated like dashboard warning lights when engaged).
	graphTop := 130
	iconW := 30
	gx := 12
	gw := width - 24 - iconW - 8
	gh := height - graphTop - 10
	if gh >= 10 && gw >= 16 {
		drawPedalGraph(pixels, width, height, gx, graphTop, gw, gh, hud, green, red, stroke)

		iconX := gx + gw + 8
		iconSize := iconW
		if half := (gh - 6) / 2; half < iconSize {
			iconSize = half
		}
		if iconSize >= 10 {
			drawTellTale(pixels, width, height, iconX, graphTop, iconSize, "C", hud.Clutch, amber, stroke, muted)
			drawTellTale(pixels, width, height, iconX, graphTop+iconSize+6, iconSize, "P", hud.HandBrake, red, stroke, muted)
		}
	}
}

// drawPedalGraph plots throttle (green) and brake (red) over the last few
// seconds: time runs left (oldest) to right (newest), value bottom (0) to top
// (full). Newest sample is pinned to the right so the trace scrolls steadily as
// the buffer fills.
func drawPedalGraph(pixels []uint32, width, height, x, y, w, h int, hud HUD, throttleColor, brakeColor, border color.RGBA) {
	fillRect(pixels, width, height, x, y, w, h, color.RGBA{R: 18, G: 22, B: 27, A: border.A})
	fillRect(pixels, width, height, x, y+h/2, w, 1, color.RGBA{R: border.R, G: border.G, B: border.B, A: border.A / 2})
	drawRect(pixels, width, height, x, y, w, h, border)

	drawText(pixels, width, height, x+3, y+3, 1, "T", throttleColor)
	drawText(pixels, width, height, x+3, y+11, 1, "B", brakeColor)

	drawSeries(pixels, width, height, x, y, w, h, hud.BrakeHistory, hud.HistoryCap, brakeColor)
	drawSeries(pixels, width, height, x, y, w, h, hud.ThrottleHistory, hud.HistoryCap, throttleColor)
}

func drawSeries(pixels []uint32, width, height, x, y, w, h int, series []float64, capacity int, c color.RGBA) {
	if len(series) < 2 {
		return
	}
	if capacity < 2 {
		capacity = len(series)
	}
	step := float64(w-1) / float64(capacity-1)
	point := func(i int) (int, int) {
		// Pin the newest sample (last index) to the right edge.
		px := x + (w - 1) - int(float64(len(series)-1-i)*step)
		v := series[i]
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		py := y + int(float64(h-1)*(1-v))
		return px, py
	}
	prevX, prevY := point(0)
	for i := 1; i < len(series); i++ {
		cx, cy := point(i)
		drawLine(pixels, width, height, prevX, prevY, cx, cy, 2, c)
		prevX, prevY = cx, cy
	}
}

// drawTellTale draws a round dashboard indicator whose face fades from dim
// (level 0, control released) to onColor (level 1, fully applied), like a
// warning light glowing in proportion to how hard the pedal/lever is pressed.
func drawTellTale(pixels []uint32, width, height, x, y, size int, label string, level float64, onColor, border, muted color.RGBA) {
	if level < 0 {
		level = 0
	} else if level > 1 {
		level = 1
	}
	cx := x + size/2
	cy := y + size/2
	r := size / 2

	scale := 2
	if size >= 26 {
		scale = 3
	}
	glyphW := 5 * scale
	glyphH := 7 * scale
	tx := cx - glyphW/2
	ty := cy - glyphH/2

	dim := color.RGBA{R: 22, G: 26, B: 31, A: onColor.A}
	dark := color.RGBA{R: 12, G: 16, B: 20, A: onColor.A}

	fillCircle(pixels, width, height, cx, cy, r, border)
	fillCircle(pixels, width, height, cx, cy, r-2, lerpRGBA(dim, onColor, level))
	// Letter shifts from muted (dim) to dark (lit) so it stays legible against
	// the brightening face.
	drawText(pixels, width, height, tx, ty, scale, label, lerpRGBA(muted, dark, level))
}

func lerpRGBA(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	lerp := func(from, to uint8) uint8 {
		return uint8(float64(from) + (float64(to)-float64(from))*t)
	}
	return color.RGBA{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: b.A}
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

// drawLine plots a thickness×thickness pen along a Bresenham line between two
// points, clipped to the surface by fillRect.
func drawLine(pixels []uint32, width, height, x0, y0, x1, y1, thickness int, c color.RGBA) {
	if thickness < 1 {
		thickness = 1
	}
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 > x1 {
		sx = -1
	}
	sy := 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	off := thickness / 2
	for {
		fillRect(pixels, width, height, x0-off, y0-off, thickness, thickness, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func fillCircle(pixels []uint32, width, height, cx, cy, r int, c color.RGBA) {
	if r <= 0 {
		return
	}
	packed := packPremul(c)
	for dy := -r; dy <= r; dy++ {
		yy := cy + dy
		if yy < 0 || yy >= height {
			continue
		}
		span := int(math.Sqrt(float64(r*r - dy*dy)))
		x0 := cx - span
		x1 := cx + span
		if x0 < 0 {
			x0 = 0
		}
		if x1 >= width {
			x1 = width - 1
		}
		row := yy * width
		for xx := x0; xx <= x1; xx++ {
			pixels[row+xx] = packed
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
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

func drawSteering(pixels []uint32, width, height int, x, y, steeringSize int, opacity float64, steeringPixels []uint32) {
	for sy := 0; sy < steeringSize; sy++ {
		for sx := 0; sx < steeringSize; sx++ {
			dx := x + sx
			dy := y + sy
			if dx < 0 || dx >= width || dy < 0 || dy >= height {
				continue
			}

			srcPx := steeringPixels[sy*steeringSize+sx]
			if srcPx == 0 {
				continue
			}

			a := (srcPx >> 24) & 0xff
			if a == 0 {
				continue
			}

			blend := blendAlpha(pixels[dy*width+dx], srcPx, uint8(float64(a)*opacity))
			pixels[dy*width+dx] = blend
		}
	}
}

func blendAlpha(dst, src uint32, opacity uint8) uint32 {
	dstA := (dst >> 24) & 0xff
	dstR := (dst >> 16) & 0xff
	dstG := (dst >> 8) & 0xff
	dstB := dst & 0xff

	srcA := ((src >> 24) & 0xff) * uint32(opacity) / 255
	srcR := ((src >> 16) & 0xff)
	srcG := ((src >> 8) & 0xff)
	srcB := (src & 0xff)

	outA := dstA + srcA - dstA*srcA/255
	if outA == 0 {
		return 0
	}

	outR := (dstR*dstA + srcR*srcA) / outA
	outG := (dstG*dstA + srcG*srcA) / outA
	outB := (dstB*dstA + srcB*srcA) / outA

	return (outA << 24) | (outR << 16) | (outG << 8) | outB
}
