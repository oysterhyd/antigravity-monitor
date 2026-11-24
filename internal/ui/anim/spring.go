package anim

import "math"

// SpringConfig configures a damped harmonic oscillator.
type SpringConfig struct {
	Stiffness float64
	Damping   float64
	Precision float64
}

// DefaultSpringConfig matches docs/prototype.html (stiffness 170, damping 22).
func DefaultSpringConfig() SpringConfig {
	return SpringConfig{
		Stiffness: 170,
		Damping:   22,
		Precision: 1e-4,
	}
}

// SnappySpringConfig is tuned for crisp interactive controls (crosshair, toggles).
func SnappySpringConfig() SpringConfig {
	return SpringConfig{
		Stiffness: 280,
		Damping:   26,
		Precision: 1e-4,
	}
}

// SmoothSpringConfig is tuned for gentle non-overshooting transitions.
func SmoothSpringConfig() SpringConfig {
	return SpringConfig{
		Stiffness: 140,
		Damping:   24,
		Precision: 1e-4,
	}
}

func (c SpringConfig) normalized() SpringConfig {
	if c.Stiffness <= 0 {
		c.Stiffness = 170
	}
	if c.Damping <= 0 {
		c.Damping = 22
	}
	if c.Precision <= 0 {
		c.Precision = 1e-4
	}
	return c
}

// Spring is a frame-rate-independent damped spring interpolator.
// Exported for reuse across Track 2 (Desktop) and Track 3 (Tray).
type Spring struct {
	value   float64
	target  float64
	vel     float64
	cfg     SpringConfig
	settled bool
}

// NewSpring creates a Spring settled at initial.
func NewSpring(initial float64, cfg SpringConfig) *Spring {
	return &Spring{
		value:   initial,
		target:  initial,
		cfg:     cfg.normalized(),
		settled: true,
	}
}

// SetTarget updates the spring destination without resetting velocity.
func (s *Spring) SetTarget(target float64) {
	if math.IsNaN(target) || math.IsInf(target, 0) {
		return
	}
	if math.Abs(target-s.target) <= s.cfg.Precision*0.1 && s.settled {
		s.target = target
		s.value = target
		return
	}
	s.target = target
	if math.Abs(s.target-s.value) > s.cfg.Precision || math.Abs(s.vel) > s.cfg.Precision {
		s.settled = false
	}
}

// Jump snaps the spring immediately to value with zero velocity.
func (s *Spring) Jump(value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	s.value = value
	s.target = value
	s.vel = 0
	s.settled = true
}

// Step advances the spring by dtSec seconds.
// Returns true if the spring is still moving and needs another animation frame.
func (s *Spring) Step(dtSec float64) bool {
	if s.settled {
		return false
	}
	if dtSec <= 0 {
		return !s.settled
	}
	if dtSec > 0.05 {
		dtSec = 0.05
	}

	const maxSubStep = 1.0 / 120.0
	remaining := dtSec
	for remaining > 0 {
		h := remaining
		if h > maxSubStep {
			h = maxSubStep
		}
		remaining -= h

		diff := s.target - s.value
		acc := s.cfg.Stiffness*diff - s.cfg.Damping*s.vel
		s.vel += acc * h
		s.value += s.vel * h
	}

	if math.Abs(s.target-s.value) <= s.cfg.Precision && math.Abs(s.vel) <= s.cfg.Precision {
		s.value = s.target
		s.vel = 0
		s.settled = true
		return false
	}
	return true
}

func (s *Spring) Value() float64    { return s.value }
func (s *Spring) Target() float64   { return s.target }
func (s *Spring) Velocity() float64 { return s.vel }
func (s *Spring) Settled() bool     { return s.settled }

// SpringBank manages a keyed collection of numeric springs (e.g. KPI counters).
// When all springs settle, StepAll returns false so the UI sleeps at ~0% GPU.
type SpringBank struct {
	cfg     SpringConfig
	springs map[string]*Spring
}

// NewSpringBank creates a bank of springs sharing cfg.
func NewSpringBank(cfg SpringConfig) *SpringBank {
	return &SpringBank{
		cfg:     cfg.normalized(),
		springs: make(map[string]*Spring, 16),
	}
}

// Set sets the target for key; on first call, initializes settled at target so
// initial render does not count up from zero.
func (b *SpringBank) Set(key string, target float64) float64 {
	s, ok := b.springs[key]
	if !ok {
		s = NewSpring(target, b.cfg)
		b.springs[key] = s
		return s.Value()
	}
	s.SetTarget(target)
	return s.Value()
}

// Jump snaps key immediately to value.
func (b *SpringBank) Jump(key string, value float64) {
	s, ok := b.springs[key]
	if !ok {
		b.springs[key] = NewSpring(value, b.cfg)
		return
	}
	s.Jump(value)
}

// Value returns the current animated value for key, or fallback if absent.
func (b *SpringBank) Value(key string, fallback float64) float64 {
	if s, ok := b.springs[key]; ok {
		return s.Value()
	}
	return fallback
}

// StepAll advances every spring by dtSec and reports whether any spring is still moving.
func (b *SpringBank) StepAll(dtSec float64) bool {
	active := false
	for _, s := range b.springs {
		if s.Step(dtSec) {
			active = true
		}
	}
	return active
}

// Active reports whether any spring in the bank has not yet settled.
func (b *SpringBank) Active() bool {
	for _, s := range b.springs {
		if !s.Settled() {
			return true
		}
	}
	return false
}
