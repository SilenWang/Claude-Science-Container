package gui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// Shared building blocks: type sizes relative to the theme, field rows, and
// the two button weights the window uses. Everything here is view-only so the
// tabs can be rendered and measured without a desktop.

// fs is a type size in steps from the theme's own, so the window follows the
// desktop's text size setting.
func fs(c *ui.Context, delta float32) float32 { return c.Theme().FontSize + delta }

// pane bounds the content column so cards stay readable in a wide window,
// centered once the window grows past it.
const paneWidth = 760

func pane(c *ui.Context) ui.Element {
	return ui.Column(c).WidthPercent(100).MaxWidth(paneWidth).AlignSelf(ui.Center)
}

func paneRow(c *ui.Context) ui.Element {
	return ui.Row(c).WidthPercent(100).MaxWidth(paneWidth).AlignSelf(ui.Center)
}

// field stacks a quiet label above its control. The control is built inside
// the column, so it becomes the column's child rather than the parent's.
func field(c *ui.Context, label string, build func(*ui.Context) ui.Element) ui.Element {
	t := c.Theme()
	col := ui.Column(c).Gap(4).Grow(1)
	col.Children(func() {
		ui.Text(c, label).FontSize(fs(c, -3)).TextColor(t.TextMuted).SingleLine()
		build(c).WidthPercent(100)
	})
	return col
}

// fieldPair puts two fields side by side, each taking half the row.
func fieldPair(c *ui.Context, l1 string, b1 func(*ui.Context) ui.Element, l2 string, b2 func(*ui.Context) ui.Element) {
	ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
		field(c, l1, b1)
		field(c, l2, b2)
	})
}

// textField is the common single-line input: a key for its state, a value to
// edit, and placeholder text for what belongs there.
func textField(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return ui.TextInput(c.Key(key), value).Placeholder(placeholder)
}

// hint is a quiet line under a group of fields.
func hint(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(fs(c, -3)).TextColor(c.Theme().TextMuted).Wrap()
}

// actionButton is the one button that matters: the accent fill.
func actionButton(c *ui.Context, label string) ui.Element {
	return ui.PrimaryButton(c, label).Padding(7, 14).Radius(7).FontWeight(500)
}

// ghostButton is a quiet secondary action: text only, a fill on hover.
func ghostButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Padding(7, 10).Radius(7).TextColor(t.TextMuted).FontWeight(500)
	b.Children(func() { ui.Text(c, label).FontSize(fs(c, -1)) })
	if b.Hovered() {
		b.Background(t.SurfacePressed)
	}
	return b
}

// dangerButton is a quiet destructive action, red only on hover so it does
// not compete with the primary action.
func dangerButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ghostButton(c, label)
	b.TextColor(t.Danger.Alpha(0.85))
	if b.Hovered() {
		b.Background(t.Danger.Alpha(0.1))
	}
	return b
}

// card frames one server or one group of settings.
func card(c *ui.Context) ui.Element {
	t := c.Theme()
	e := ui.Column(c).Padding(14, 16).Gap(10).Radius(10).
		Background(t.Surface).Border(1, t.Border)
	return e
}

// sectionTitle names a group inside a tab.
func sectionTitle(c *ui.Context, title, detail string) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
		ui.Text(c, title).FontSize(fs(c, 0)).Bold().SingleLine()
		if detail != "" {
			ui.Text(c, detail).FontSize(fs(c, -3)).TextColor(t.TextMuted).SingleLine()
		}
	})
}

// statusDot is a state in one glance: quiet while idle, spinning while
// starting, green when up, red when it failed. The words beside it name the
// state, so the dot is decoration to a screen reader.
func statusDot(c *ui.Context, phase phaseKind) {
	t := c.Theme()
	switch phase {
	case phaseBusy:
		ui.Spinner(c).Shrink(0).AccessHidden(true)
	case phaseOK:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Success).Shrink(0).AccessHidden(true)
	case phaseFailed:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Danger).Shrink(0).AccessHidden(true)
	default:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Border).Border(1, t.TextMuted.Alpha(0.35)).Shrink(0).AccessHidden(true)
	}
}

// dotBox gives the dot the cap height of the name line it sits beside.
func dotBox(c *ui.Context, phase phaseKind) ui.Element {
	box := ui.Box(c).Shrink(0).Width(9).Height(fs(c, 6)).Center()
	box.Children(func() { statusDot(c, phase) })
	return box
}

// detailToggle reveals technical text on request only. It returns whether it
// is expanded now.
func detailToggle(c *ui.Context, key, detail string, shown bool) bool {
	if detail == "" {
		return false
	}
	t := c.Theme()

	toggle := ui.ButtonBase(c).Key(key).Padding(3, 4).MarginStart(-4).Radius(5).
		TextColor(t.TextMuted).AlignSelf(ui.Start).
		Tooltip("显示或隐藏原始信息")
	toggle.Children(func() {
		ui.Text(c, "详细信息").FontSize(fs(c, -3))
		icon := ui.Icon(c, chevron).Size(12, 12).TextColor(t.TextMuted).Shrink(0)
		if !shown {
			icon.Rotate(-90)
		}
	})
	if toggle.Hovered() {
		toggle.Background(t.SurfaceHover)
	}
	if toggle.Clicked() {
		shown = !shown
	}
	if shown {
		ui.Text(c, detail).FontSize(fs(c, -3)).Font("monospace").LineHeight(1.5).
			TextColor(t.TextMuted).Wrap().Selectable().
			Padding(6, 8).Radius(6).Background(t.SurfaceHover).
			AlignSelf(ui.Stretch).Margin(2, 0)
	}
	return shown
}

// banner is the one place save and validation problems surface: a sentence,
// with the technical text folded behind 详细信息.
func banner(c *ui.Context, text, detail string, shown *bool) {
	if text == "" {
		return
	}
	t := c.Theme()
	ui.Column(c).Gap(4).Padding(10, 12).Radius(8).
		Background(t.Danger.Alpha(0.08)).Border(1, t.Danger.Alpha(0.25)).
		AlignSelf(ui.Stretch).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Icon(c, alertIcon).Size(16, 16).TextColor(t.Danger).Shrink(0)
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, text).FontSize(fs(c, -2)).TextColor(t.Text).Wrap()
				if detail != "" {
					*shown = detailToggle(c, "banner-detail", detail, *shown)
				}
			})
		})
	})
}

// joinAddress is how a server identifies itself: who to connect as, and where.
func joinAddress(user, host string) string {
	if host == "" {
		return "未配置地址"
	}
	if user == "" {
		return host
	}
	return fmt.Sprintf("%s@%s", user, host)
}
