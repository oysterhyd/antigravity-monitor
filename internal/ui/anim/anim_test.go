package anim

import (
	"math"
	"testing"
	"time"
)

func TestSpringConvergesAndSettles(t *testing.T) {
	s := NewSpring(0, DefaultSpringConfig())
	if !s.Settled() {
		t.Fatal("initial spring should be settled")
	}

	s.SetTarget(100)
	if s.Settled() {
		t.Fatal("spring should become active after target change")
	}

	const dt = 1.0 / 60.0
	steps := 0
	for s.Step(dt) && steps < 600 {
		steps++
	}
	if !s.Settled() {
		t.Fatalf("spring did not settle after %d steps: val=%f vel=%f", steps, s.Value(), s.Velocity())
	}
	if math.Abs(s.Value()-100) > 1e-4 {
		t.Fatalf("expected 100, got %f", s.Value())
	}
	if s.Step(dt) {
		t.Fatal("settled spring must return false on Step for 0% idle GPU")
	}
}

func TestSpringBankInitializesSettled(t *testing.T) {
	bank := NewSpringBank(DefaultSpringConfig())
	v := bank.Set("tps", 126.4)
	if v != 126.4 {
		t.Fatalf("first Set should snap to target, got %f", v)
	}
	if bank.Active() {
		t.Fatal("bank should stay idle after initial Set")
	}

	bank.Set("tps", 142.0)
	if !bank.Active() {
		t.Fatal("bank should be active after target change")
	}
	for i := 0; i < 400 && bank.StepAll(1.0/60.0); i++ {
	}
	if bank.Active() {
		t.Fatal("bank should settle cleanly")
	}
	if math.Abs(bank.Value("tps", 0)-142.0) > 1e-3 {
		t.Fatalf("expected 142.0, got %f", bank.Value("tps", 0))
	}
}

func TestCurveMorpherMorphsAndSettles(t *testing.T) {
	m := NewCurveMorpher(18)
	m.SetTargets([]float64{10, 20, 30, 40}, 50)
	tail := m.Tail(nil, 4)
	if len(tail) != 4 || math.Abs(float64(tail[3])-0.8) > 1e-4 {
		t.Fatalf("initial targets should snap immediately: %v", tail)
	}
	if m.Step(1.0 / 60.0) {
		t.Fatal("initial morpher should be idle")
	}

	m.SetTargets([]float64{50, 40, 30, 10}, 50)
	m.SetCrosshairTarget(0.25, true)
	m.SetCrosshairTarget(0.75, false)

	active := false
	for i := 0; i < 300; i++ {
		if !m.Step(1.0 / 60.0) {
			break
		}
		active = true
	}
	if !active {
		t.Fatal("expected morpher to animate after target change")
	}
	if m.Step(1.0 / 60.0) {
		t.Fatal("expected morpher to settle to idle")
	}
	tail = m.Tail(tail, 4)
	if math.Abs(float64(tail[0])-1.0) > 1e-3 || math.Abs(float64(tail[3])-0.2) > 1e-3 {
		t.Fatalf("unexpected settled tail: %v", tail)
	}
	cx, ok := m.CrosshairFrac()
	if !ok || math.Abs(cx-0.75) > 1e-3 {
		t.Fatalf("unexpected crosshair: ok=%v cx=%f", ok, cx)
	}
}

func TestPulseDecaysToZero(t *testing.T) {
	var p Pulse
	t0 := time.Unix(1000, 0)
	if p.Active(t0) {
		t.Fatal("untriggered pulse should be inactive")
	}
	p.Trigger(t0, 800*time.Millisecond)
	if a := p.Alpha(t0); a < 0.99 {
		t.Fatalf("expected ~1.0 at start, got %f", a)
	}
	if a := p.Alpha(t0.Add(400 * time.Millisecond)); a <= 0 || a >= 0.5 {
		t.Fatalf("expected cubic ease-out ~0.125 at midpoint, got %f", a)
	}
	if p.Active(t0.Add(800 * time.Millisecond)) {
		t.Fatal("pulse should be inactive after duration")
	}
}
