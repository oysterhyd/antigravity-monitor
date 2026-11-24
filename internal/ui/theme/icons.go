package theme

import "github.com/egoist/mygo/ui"

func monoIcon(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` +
			body +
			`</svg>`,
	))
}

// Monochrome SF-style linear vector icons matching docs/prototype.html.
// Zero colored block badges or noisy stickers.
var (
	IconTree = monoIcon(
		`<circle cx="12" cy="5" r="2.5"/><circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="19" r="2.5"/>` +
			`<path d="M12 7.5V13M12 13L6 16.5M12 13L18 16.5"/>`,
	)

	IconContext = monoIcon(
		`<rect x="3" y="4" width="18" height="16" rx="4"/><path d="M9 4v16"/>`,
	)

	IconCost = monoIcon(
		`<path d="M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/>`,
	)

	IconCache = monoIcon(
		`<path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/>`,
	)

	IconClock = monoIcon(
		`<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	)

	IconBolt = monoIcon(
		`<path d="M13 2L3 14h7l-1 8 10-12h-7l1-8z"/>`,
	)

	IconChevronDown = monoIcon(
		`<path d="M6 9l6 6 6-6"/>`,
	)

	IconChevronLeft = monoIcon(
		`<path d="M15 18l-6-6 6-6"/>`,
	)

	IconChevronRight = monoIcon(
		`<path d="M9 18l6-6-6-6"/>`,
	)

	IconSettings = monoIcon(
		`<circle cx="12" cy="12" r="3"/>` +
			`<path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.604.852.997 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>`,
	)

	IconSun = monoIcon(
		`<circle cx="12" cy="12" r="4"/>` +
			`<path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>`,
	)

	IconMoon = monoIcon(
		`<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>`,
	)

	IconBurst = monoIcon(
		`<path d="M12 5v14M5 12h14"/>`,
	)
)
