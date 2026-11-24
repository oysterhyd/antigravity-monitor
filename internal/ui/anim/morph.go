package anim

import (
	"math"
	"time"
)

// CurveMorpher smoothly morphs N normalized [0,1] curve samples between metrics/scopes
// and glides the active crosshair along X with spring physics.
type CurveMorpher struct {
	slots      int
	curr       []float64
	target     []float64
	initialized bool
	settled    bool

	crossActive bool
	crossX      *Spring
}

// NewCurveMorpher creates a morpher with the given slot count (default 18).
func NewCurveMorpher(slots int) *CurveMorpher {
	if slots <= 0 {
		slots = 18
	}
	curr := make([]float64, slots)
	target := make([]float64, slots)
	for i := range curr {
		curr[i] = 0.5
		target[i] = 0.5
	}
	return &CurveMorpher{
		slots:   slots,
		curr:    curr,
		target:  target,
		settled: true,
		crossX:  NewSpring(0, SnappySpringConfig()),
	}
}

// Slots returns the fixed capacity of the morpher.
func (m *CurveMorpher) Slots() int { return m.slots }

// SetTargets updates the normalized target series from raw values and maxVal.
// Values are right-aligned into the N slots, matching docs/prototype.html syncChartTargets.
func (m *CurveMorpher) SetTargets(values []float64, maxVal float64) {
	if maxVal <= 0 {
		maxVal = 1
	}
	n := len(values)
	if n > m.slots {
		values = values[n-m.slots:]
		n = m.slots
	}
	offset := m.slots - n
	changed := false
	for i := 0; i < m.slots; i++ {
		var tv float64
		if idx := i - offset; idx >= 0 {
			tv = values[idx] / maxVal
			if tv < 0 {
				tv = 0
			}
			if tv > 1 {
				tv = 1
			}
		}
		if !m.initialized {
			m.curr[i] = tv
			m.target[i] = tv
		} else {
			if math.Abs(tv-m.target[i]) > 1e-4 {
				changed = true
			}
			m.target[i] = tv
		}
	}
	if !m.initialized {
		m.initialized = true
		m.settled = true
		return
	}
	if changed {
		m.settled = false
	}
}

// SetCrosshairTarget moves the vertical crosshair smoothly toward x (in [0, 1] plot fraction).
func (m *CurveMorpher) SetCrosshairTarget(fracX float64, snap bool) {
	if !m.crossActive || snap {
		m.crossActive = true
		m.crossX.Jump(fracX)
		return
	}
	m.crossX.SetTarget(fracX)
}

// ClearCrosshair resets the crosshair so the next target snaps cleanly (e.g., on conversation switch).
func (m *CurveMorpher) ClearCrosshair() {
	m.crossActive = false
}

// CrosshairFrac returns the current morphed crosshair position in [0, 1].
func (m *CurveMorpher) CrosshairFrac() (float64, bool) {
	return m.crossX.Value(), m.crossActive
}

// Step advances point morphing and crosshair spring by dtSec.
// Returns true while any point or the crosshair is still moving.
func (m *CurveMorpher) Step(dtSec float64) bool {
	if dtSec <= 0 {
		return !m.settled || (m.crossActive && !m.crossX.Settled())
	}
	if dtSec > 0.05 {
		dtSec = 0.05
	}

	active := false
	if !m.settled {
		// Rate ~10.5/s matches 0.16 blend per 60Hz frame in prototype.html.
		alpha := 1.0 - math.Exp(-10.5*dtSec)
		stillMoving := false
		for i := 0; i < m.slots; i++ {
			diff := m.target[i] - m.curr[i]
			if math.Abs(diff) < 0.0005 {
				m.curr[i] = m.target[i]
			} else {
				m.curr[i] += diff * alpha
				stillMoving = true
			}
		}
		m.settled = !stillMoving
		active = stillMoving
	}

	if m.crossActive && m.crossX.Step(dtSec) {
		active = true
	}
	return active
}

// Tail writes the last count morphed normalized values into dst (or allocates if too small).
func (m *CurveMorpher) Tail(dst []float32, count int) []float32 {
	if count <= 0 {
		return dst[:0]
	}
	if count > m.slots {
		count = m.slots
	}
	offset := m.slots - count
	if cap(dst) < count {
		dst = make([]float32, count)
	} else {
		dst = dst[:count]
	}
	for i := 0; i < count; i++ {
		dst[i] = float32(m.curr[offset+i])
	}
	return dst
}

// Pulse represents a one-shot decaying highlight ripple (e.g. req-ping on new telemetry).
type Pulse struct {
	started  time.Time
	duration time.Duration
}

// Trigger starts a pulse at now lasting duration (default 800ms).
func (p *Pulse) Trigger(now time.Time, duration time.Duration) {
	if duration <= 0 {
		duration = 800 * time.Millisecond
	}
	p.started = now
	p.duration = duration
}

// Alpha returns the current ease-out intensity in [0, 1] at now.
func (p *Pulse) Alpha(now time.Time) float32 {
	if p.started.IsZero() || p.duration <= 0 {
		return 0
	}
	elapsed := now.Sub(p.started)
	if elapsed < 0 || elapsed >= p.duration {
		return 0
	}
	t := float64(elapsed) / float64(p.duration)
	inv := 1.0 - t
	return float32(inv * inv * inv)
}

// Active reports whether the pulse is still visibly decaying at now.
func (p *Pulse) Active(now time.Time) bool {
	return p.Alpha(now) > 0.005
}
