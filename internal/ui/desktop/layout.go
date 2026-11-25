package desktop

import "github.com/egoist/mygo/ui"

// layoutMetrics holds smooth responsive dimensions derived from the window viewport.
// At the default 900x590 size (<= 980x660), scaleX=scaleY=0 so every pixel matches
// the crisp compact baseline. On enlarged or maximized screens (e.g. 1920x1080),
// the sidebar widens gracefully and the right canvas caps to balanced editorial
// proportions centered inside the Liquid Glass stage.
type layoutMetrics struct {
	scaleX          float32
	scaleY          float32
	scale           float32
	outerPad        float32
	layoutGap       float32
	sidebarW        float32
	canvasMaxW      float32
	canvasMaxH      float32
	stageCenter     bool
	headerRightPadT float32

	sectionGap       float32
	histSectionGap   float32
	headerTitleSize  float32
	heroPctSize      float32
	heroBarH         float32
	kpiValueSize     float32
	kpiHistValueSize float32
	kpiPadY          float32
	kpiPadX          float32
	cardPadY         float32
	cardPadX         float32
}

func computeLayout(winW, winH float32, bar ui.TitleBar) layoutMetrics {
	const (
		baseW      = float32(980)
		maxScaleW  = float32(1560)
		baseH      = float32(660)
		maxScaleH  = float32(940)
		canvasCapW = float32(1240)
		canvasCapH = float32(860)
	)

	sx := clamp01f((winW - baseW) / (maxScaleW - baseW))
	sy := clamp01f((winH - baseH) / (maxScaleH - baseH))
	s := min(sx, sy)

	outerPad := 12 + 6*s
	layoutGap := 12 + 6*sx
	sideW := sidebarWidth + 68*sx

	rightPaneW := winW - outerPad*2 - sideW - layoutGap
	innerH := winH - outerPad*2
	stageCenter := rightPaneW > canvasCapW+1 || innerH > canvasCapH+1

	// Only reserve top-right titlebar clearance when the header sits close to
	// the window's top-right native min/max/close controls.
	rightTopPad := float32(0)
	if bar.Right > 0 && bar.Height > 0 {
		topMargin := outerPad
		if innerH > canvasCapH {
			topMargin += (innerH - canvasCapH) / 2
		}
		rightMargin := outerPad
		if rightPaneW > canvasCapW {
			rightMargin += (rightPaneW - canvasCapW) / 2
		}
		if topMargin < bar.Height+6 && rightMargin < bar.Right+12 {
			rightTopPad = max(float32(18), bar.Height-10)
		}
	}

	return layoutMetrics{
		scaleX:           sx,
		scaleY:           sy,
		scale:            s,
		outerPad:         outerPad,
		layoutGap:        layoutGap,
		sidebarW:         sideW,
		canvasMaxW:       canvasCapW,
		canvasMaxH:       canvasCapH,
		stageCenter:      stageCenter,
		headerRightPadT:  rightTopPad,
		sectionGap:       12 + 4*s,
		histSectionGap:   11 + 4*s,
		headerTitleSize:  20 + 4*s,
		heroPctSize:      20 + 4*s,
		heroBarH:         10 + 2*s,
		kpiValueSize:     23 + 4*s,
		kpiHistValueSize: 21 + 4*s,
		kpiPadY:          15 + 3*s,
		kpiPadX:          15 + 4*s,
		cardPadY:         12 + 4*s,
		cardPadX:         16 + 4*s,
	}
}

func clamp01f(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
