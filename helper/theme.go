package main

import "github.com/egoist/mygo/ui"

// The palette keeps the window quiet: a warm neutral ground, one accent for
// the action that matters, and color only where it carries meaning (a
// connection that is up, one that failed). Text pairs are checked against
// WCAG AA: body and status text at 4.5:1 or better on both grounds.
func lightTheme() *ui.Theme {
	t := ui.LightTheme()
	t.Background = ui.Hex("#fbfaf8")
	t.Surface = ui.Hex("#ffffff")
	t.SurfaceHover = ui.Hex("#f7f5f1")
	t.SurfacePressed = ui.Hex("#efece6")
	t.Border = ui.Hex("#e6e1d8")
	t.Text = ui.Hex("#1f1d1a")
	t.TextMuted = ui.Hex("#6b6459")
	t.Accent = ui.Hex("#a84e2b")
	t.AccentHover = ui.Hex("#9c4a28")
	t.AccentPressed = ui.Hex("#8d4224")
	t.AccentText = ui.Hex("#ffffff")
	t.Danger = ui.Hex("#b3261e")
	t.Warning = ui.Hex("#96631a")
	t.Success = ui.Hex("#2e7d51")
	t.Selection = ui.RGBA(168, 78, 43, 0.22)
	t.Focus = ui.RGBA(168, 78, 43, 0.55)
	t.Scrollbar = ui.RGBA(31, 29, 26, 0.25)
	t.Radius = 8
	t.Spacing = 4
	t.FontSize = 14
	return t
}

func darkTheme() *ui.Theme {
	t := ui.DarkTheme()
	t.Background = ui.Hex("#201e1b")
	t.Surface = ui.Hex("#26241f")
	t.SurfaceHover = ui.Hex("#2d2a25")
	t.SurfacePressed = ui.Hex("#35312b")
	t.Border = ui.Hex("#3a362f")
	t.Text = ui.Hex("#f2ede6")
	t.TextMuted = ui.Hex("#a79f92")
	t.Accent = ui.Hex("#e08a6a")
	t.AccentHover = ui.Hex("#eda183")
	t.AccentPressed = ui.Hex("#d97a58")
	t.AccentText = ui.Hex("#201e1b")
	t.Danger = ui.Hex("#f08a80")
	t.Warning = ui.Hex("#e3b25f")
	t.Success = ui.Hex("#6fbf95")
	t.Selection = ui.RGBA(224, 138, 106, 0.3)
	t.Focus = ui.RGBA(224, 138, 106, 0.6)
	t.Scrollbar = ui.RGBA(242, 237, 230, 0.28)
	t.Radius = 8
	t.Spacing = 4
	t.FontSize = 14
	return t
}

func themeFor(dark bool) *ui.Theme {
	if dark {
		return darkTheme()
	}
	return lightTheme()
}

// adaptedTheme follows the desktop's accessibility settings: a larger text
// scale, and stronger borders and secondary text in high contrast. The
// accent stays the launcher's own, so the window keeps one visual identity.
func adaptedTheme(c *ui.Context, dark bool) *ui.Theme {
	p := c.Preferences()
	t := themeFor(dark)
	if p.HighContrast {
		t.Border = t.Border.Mix(t.Text, 0.45)
		t.TextMuted = t.TextMuted.Mix(t.Text, 0.4)
		t.Focus.A = 255
	}
	if s := p.TextScale; s > 0 && s != 1 {
		t.FontSize *= s
	}
	return t
}
