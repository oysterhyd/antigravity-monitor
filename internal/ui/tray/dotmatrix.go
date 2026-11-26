package tray

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// dotMatrixRenderer paints the 34x8 Pulse live dot-matrix onto a GPU-backed
// ui.Canvas and blits it inside Element.Draw with crisp HiDPI scaling.
type dotMatrixRenderer struct {
	canvas     *ui.Canvas
	canvasW    int
	canvasH    int
	lastFrame  time.Time
	lastTab    Tab
	lastDark   bool
	lastHover  int
	lastSnapAt time.Time
	dirty      bool
}

func newDotMatrixRenderer() *dotMatrixRenderer {
	return &dotMatrixRenderer{
		lastHover: -1,
		dirty:     true,
	}
}

func (d *dotMatrixRenderer) markDirty() {
	d.dirty = true
}

func (d *dotMatrixRenderer) release() {
	d.canvas = nil
	d.canvasW = 0
	d.canvasH = 0
	d.dirty = true
}

func (d *dotMatrixRenderer) paint(c *ui.Context, p *ui.Painter, r ui.Rect, st *State, pal palette) {
	if r.W <= 0 || r.H <= 0 {
		return
	}

	now := p.Now()
	dt := float32(1.0 / 60.0)
	if !d.lastFrame.IsZero() {
		if elapsed := float32(now.Sub(d.lastFrame).Seconds()); elapsed > 0 && elapsed < 0.1 {
			dt = elapsed
		}
	}
	d.lastFrame = now

	animating := st.StepDotMatrix(dt)
	if animating {
		c.AnimationFrame()
	}

	scale := p.Scale()
	if scale <= 0 {
		scale = 1
	}
	pxW := max(1, int(math.Ceil(float64(r.W*scale))))
	pxH := max(1, int(math.Ceil(float64(r.H*scale))))

	sizeChanged := d.canvas == nil || d.canvasW != pxW || d.canvasH != pxH
	stateChanged := d.dirty ||
		d.lastTab != st.Tab ||
		d.lastDark != pal.Dark ||
		d.lastHover != st.HoverDotCol ||
		!d.lastSnapAt.Equal(st.Snapshot.GeneratedAt)

	if sizeChanged {
		d.canvas = ui.NewCanvas(pxW, pxH)
		d.canvasW = pxW
		d.canvasH = pxH
	}

	if sizeChanged || stateChanged || animating {
		d.lastTab = st.Tab
		d.lastDark = pal.Dark
		d.lastHover = st.HoverDotCol
		d.lastSnapAt = st.Snapshot.GeneratedAt
		d.dirty = false

		d.canvas.Draw(p, func(cp *ui.Painter) {
			drawDots(cp, float32(pxW), float32(pxH), scale, st, pal)
		})
	}

	p.Image(d.canvas.Bitmap(), r, ui.FillBox)
}

func drawDots(cp *ui.Painter, w, h, scale float32, st *State, pal palette) {
	gapX := w / DotCols
	gapY := h / DotRows
	rad := min(gapX, gapY) * 0.28

	for c := 0; c < DotCols; c++ {
		aH := int(math.Round(float64(st.DotLevelsA[c])))
		bH := int(math.Round(float64(st.DotLevelsB[c])))
		isHover := c == st.HoverDotCol

		if isHover {
			colRect := ui.Rect{
				X: float32(c)*gapX + scale,
				Y: 0,
				W: max(1, gapX-2*scale),
				H: h,
			}
			cp.Fill(colRect, pal.HoverColBg, 4*scale)
		}

		dotRad := rad
		if isHover {
			dotRad = rad * 1.18
		}
		diam := dotRad * 2

		for row := 0; row < DotRows; row++ {
			fromBot := DotRows - 1 - row
			fill := pal.DotIdle
			if st.Tab == TabAgents {
				if fromBot < aH {
					fill = pal.Accent
				} else if fromBot < bH {
					fill = pal.Amber
				}
			} else {
				if fromBot < bH && c%2 == 1 {
					fill = pal.Amber
				} else if fromBot < aH {
					fill = pal.Accent
				}
			}

			cx := float32(c)*gapX + gapX/2
			cy := float32(row)*gapY + gapY/2
			cp.Fill(ui.Rect{
				X: cx - dotRad,
				Y: cy - dotRad,
				W: diam,
				H: diam,
			}, fill, dotRad)
		}
	}
}
