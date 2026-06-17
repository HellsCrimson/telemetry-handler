package moza

import "math"

// CurvePoint is one control point of the rev-light response curve, in
// normalised space: X is the input RPM ratio (current/max, 0..1) and Y is the
// output bar fill (0..1) that drives how many LEDs light.
type CurvePoint struct {
	X float64
	Y float64
}

// RPMCurve reshapes the linear current/max RPM ratio before it drives the
// rev-light bar. Points are control points sorted by X; the curve between them
// is a monotone cubic spline (Fritsch–Carlson), so the response is smooth and
// never overshoots below 0 or above 1. Fewer than two points reproduces the
// legacy linear behaviour exactly. Outside the first/last point the curve is
// held flat (a low-RPM dead zone before the first point, saturation after the
// last) so the user can, e.g., light the full bar before max RPM.
type RPMCurve struct {
	Points []CurvePoint
}

// apply maps an input ratio in [0,1] through the curve, returning a value in
// [0,1]. A zero-value RPMCurve (no points) is the identity.
func (c RPMCurve) apply(x float64) float64 {
	p := c.Points
	n := len(p)
	if n < 2 {
		return clamp01(x)
	}
	if x <= p[0].X {
		return clamp01(p[0].Y)
	}
	if x >= p[n-1].X {
		return clamp01(p[n-1].Y)
	}

	// Locate the segment [i, i+1] containing x.
	i := 0
	for i < n-2 && x > p[i+1].X {
		i++
	}

	m := monotoneTangents(p)
	h := p[i+1].X - p[i].X
	t := (x - p[i].X) / h
	t2 := t * t
	t3 := t2 * t
	// Cubic Hermite basis functions.
	h00 := 2*t3 - 3*t2 + 1
	h10 := t3 - 2*t2 + t
	h01 := -2*t3 + 3*t2
	h11 := t3 - t2
	y := h00*p[i].Y + h10*h*m[i] + h01*p[i+1].Y + h11*h*m[i+1]
	return clamp01(y)
}

// monotoneTangents computes the Fritsch–Carlson tangents that make the cubic
// Hermite interpolation monotone (no overshoot) for the given control points.
func monotoneTangents(p []CurvePoint) []float64 {
	n := len(p)
	secant := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		dx := p[i+1].X - p[i].X
		if dx <= 0 {
			secant[i] = 0
			continue
		}
		secant[i] = (p[i+1].Y - p[i].Y) / dx
	}

	m := make([]float64, n)
	m[0] = secant[0]
	m[n-1] = secant[n-2]
	for i := 1; i < n-1; i++ {
		if secant[i-1]*secant[i] <= 0 {
			m[i] = 0
			continue
		}
		m[i] = (secant[i-1] + secant[i]) / 2
	}

	for i := 0; i < n-1; i++ {
		if secant[i] == 0 {
			m[i] = 0
			m[i+1] = 0
			continue
		}
		a := m[i] / secant[i]
		b := m[i+1] / secant[i]
		if s := a*a + b*b; s > 9 {
			t := 3 / math.Sqrt(s)
			m[i] = t * a * secant[i]
			m[i+1] = t * b * secant[i]
		}
	}
	return m
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
