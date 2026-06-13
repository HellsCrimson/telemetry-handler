package overlay

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sync"
)

type SteeringWheel struct {
	mu       sync.RWMutex
	cache    map[int][]uint32
	size     int
	original []uint32
}

func NewSteeringWheel(size int) *SteeringWheel {
	if size < 16 {
		size = 16
	}
	if size > 256 {
		size = 256
	}

	sw := &SteeringWheel{
		cache: make(map[int][]uint32),
		size:  size,
	}
	sw.original = generateSteeringWheel(size)
	return sw
}

func LoadSteeringWheel(imagePath string, size int) (*SteeringWheel, error) {
	if size < 16 {
		size = 16
	}
	if size > 256 {
		size = 256
	}

	file, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("open steering image: %w", err)
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode steering image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width != height {
		return nil, fmt.Errorf("steering image must be square, got %dx%d", width, height)
	}

	if width != size {
		img = resizeImage(img, size)
	}

	pixels := imageToRGBA(img, size)
	sw := &SteeringWheel{
		cache:    make(map[int][]uint32),
		size:     size,
		original: pixels,
	}
	return sw, nil
}

// GetRotated returns the wheel image rotated by rotationDegrees (signed; the
// caller has already applied the car's lock-to-lock range). Results are cached
// per whole-degree to keep the render loop cheap.
func (sw *SteeringWheel) GetRotated(rotationDegrees float64) []uint32 {
	normalizedAngle := int(-rotationDegrees) % 360
	if normalizedAngle < 0 {
		normalizedAngle += 360
	}

	sw.mu.RLock()
	if cached, ok := sw.cache[normalizedAngle]; ok {
		sw.mu.RUnlock()
		return cached
	}
	sw.mu.RUnlock()

	rotated := rotateImage(sw.original, sw.size, -rotationDegrees)

	sw.mu.Lock()
	sw.cache[normalizedAngle] = rotated
	if len(sw.cache) > 36 {
		for k := range sw.cache {
			delete(sw.cache, k)
			break
		}
	}
	sw.mu.Unlock()

	return rotated
}

func generateSteeringWheel(size int) []uint32 {
	pixels := make([]uint32, size*size)
	center := float64(size) / 2.0
	radius := center - 2

	black := packPremul(color.RGBA{R: 0, G: 0, B: 0, A: 255})
	darkGray := packPremul(color.RGBA{R: 40, G: 40, B: 40, A: 255})
	gray := packPremul(color.RGBA{R: 100, G: 100, B: 100, A: 255})
	lightGray := packPremul(color.RGBA{R: 150, G: 150, B: 150, A: 255})

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist > radius {
				continue
			}

			idx := y*size + x

			if dist < radius*0.7 {
				pixels[idx] = lightGray
				angle := math.Atan2(dy, dx) * 180 / math.Pi
				if angle < 0 {
					angle += 360
				}
				if (angle > 10 && angle < 40) ||
					(angle > 140 && angle < 170) ||
					(angle > 220 && angle < 250) ||
					(angle > 320 && angle < 350) {
					pixels[idx] = gray
				}
			} else if dist < radius*0.85 {
				pixels[idx] = gray
				if dist < radius*0.8 {
					pixels[idx] = darkGray
				}
			} else {
				pixels[idx] = black
			}
		}
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist > radius*0.2 && dist < radius*0.3 {
				angle := math.Atan2(dy, dx) * 180 / math.Pi
				if angle < 0 {
					angle += 360
				}
				if (angle > 350 && angle < 360) || (angle > 0 && angle < 10) {
					idx := y*size + x
					pixels[idx] = packPremul(color.RGBA{R: 200, G: 100, B: 100, A: 255})
				}
			}
		}
	}

	return pixels
}

func rotateImage(pixels []uint32, size int, angle float64) []uint32 {
	rotated := make([]uint32, len(pixels))
	angleRad := angle * math.Pi / 180.0
	cos := math.Cos(angleRad)
	sin := math.Sin(angleRad)

	center := float64(size) / 2.0

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			srcX := (float64(x)-center)*cos - (float64(y)-center)*sin + center
			srcY := (float64(x)-center)*sin + (float64(y)-center)*cos + center

			if srcX >= 0 && srcX < float64(size) && srcY >= 0 && srcY < float64(size) {
				sx := int(srcX)
				sy := int(srcY)
				if sx >= 0 && sx < size && sy >= 0 && sy < size {
					rotated[y*size+x] = pixels[sy*size+sx]
				}
			}
		}
	}

	return rotated
}

func resizeImage(img image.Image, newSize int) image.Image {
	rgba := image.NewRGBA(image.Rect(0, 0, newSize, newSize))
	srcBounds := img.Bounds()

	for y := 0; y < newSize; y++ {
		for x := 0; x < newSize; x++ {
			srcX := srcBounds.Min.X + (x * srcBounds.Dx() / newSize)
			srcY := srcBounds.Min.Y + (y * srcBounds.Dy() / newSize)
			rgba.Set(x, y, img.At(srcX, srcY))
		}
	}
	return rgba
}

func imageToRGBA(img image.Image, size int) []uint32 {
	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)

	pixels := make([]uint32, size*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a := rgba.At(x, y).RGBA()
			pixels[y*size+x] = packPremul(color.RGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: uint8(a >> 8),
			})
		}
	}
	return pixels
}
