package main

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The window shows one card per configured target: what the tunnel is doing,
// and the single action that makes sense for that state. Anything technical —
// config paths, the raw text of a failed connection — stays behind a menu or
// a disclosure, so the surface stays about the servers.
//
// Layout: header (identity and an overflow menu), the cards, a quiet footer.
// Colors, sizes and spacing come from theme.go.

// targetUI is one card in the window: a configured target, the session
// opened from it, and what the card shows of it.
type targetUI struct {
	target Target
	sess   *session

	phase  sessionPhase
	status string
	detail string

	showDetail bool
}

// guiApp is the window's state, read and changed on the main thread only.
type guiApp struct {
	win        *mygo.Window
	configPath string
	items      []*targetUI
	err        error
	showErr    bool
}

var (
	moreIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.9"/><circle cx="12" cy="12" r="1.9"/><circle cx="19" cy="12" r="1.9"/></svg>`))
	chevron   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>`))
	alertIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 8v5"/><circle cx="12" cy="16.6" r="1.1" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="9"/></svg>`))
	plusIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>`))
)

func runGUI(configPath string) {
	app := &guiApp{configPath: configPath}
	app.reload()

	mygo.App.WhenReady(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Claude Science",
			Width:     620,
			Height:    480,
			MinWidth:  460,
			MinHeight: 340,
			Content:   ui.View(app.view),
		})
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatalf("starting the GUI: %v (use --cli for the terminal launcher)", err)
	}
}

// reload reads the config file again. A target whose settings did not change
// keeps its session, so editing one entry does not drop the tunnels of the
// others; a target that changed or went away is stopped.
func (a *guiApp) reload() {
	previous := make(map[string]*targetUI, len(a.items))
	for _, it := range a.items {
		previous[targetKey(it.target)] = it
	}

	targets, err := LoadConfig(a.configPath)
	if err != nil {
		for _, it := range a.items {
			if it.sess != nil {
				it.sess.Stop()
			}
		}
		a.err = err
		a.items = nil
		a.showErr = false
		return
	}

	a.err = nil
	items := make([]*targetUI, 0, len(targets))
	for _, target := range targets {
		key := targetKey(target)
		if old, ok := previous[key]; ok {
			// Same configuration: keep the card, and its tunnel with it.
			items = append(items, old)
			delete(previous, key)
			continue
		}
		items = append(items, &targetUI{target: target, phase: phaseIdle})
	}
	for _, it := range previous {
		if it.sess != nil {
			it.sess.Stop()
		}
	}
	a.items = items
}

// targetKey identifies a target's configuration, to tell a reloaded entry
// from a new or edited one.
func targetKey(t Target) string {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Sprintf("%v", t)
	}
	return string(data)
}

// paneWidth bounds the content column, so cards stay readable in a window
// dragged wide; the window opens narrower than this and grows to it.
const paneWidth = 720

func (a *guiApp) view(c *ui.Context) {
	// Follow the system appearance and accessibility settings, in the
	// launcher's own palette.
	c.SetTheme(adaptedTheme(c, c.Theme().Dark))

	c.OnShortcut(ui.Cmd, ui.KeyR, a.reload)
	c.OnShortcut(ui.Cmd, ui.KeyO, a.openConfigDir)
	c.OnShortcut(ui.Cmd, ui.KeyQ, mygo.App.Quit)

	ui.Column(c).Fill().Children(func() {
		a.header(c)

		switch {
		case a.err != nil:
			a.brokenState(c)
		case len(a.items) == 0:
			a.emptyState(c)
		default:
			ui.Scroll(c).Grow(1).Children(func() {
				pane(c).Gap(8).Padding(16, 22, 18, 22).Children(func() {
					for i, it := range a.items {
						a.card(c, i, it)
					}
				})
			})
			a.footer(c)
		}
	})
}

// pane is the content column of the window: as wide as the window allows,
// up to paneWidth, centered when the window is wider.
func pane(c *ui.Context) ui.Element {
	return ui.Column(c).WidthPercent(100).MaxWidth(paneWidth).AlignSelf(ui.Center)
}

// paneRow is pane's counterpart for a row of the header or footer.
func paneRow(c *ui.Context) ui.Element {
	return ui.Row(c).WidthPercent(100).MaxWidth(paneWidth).AlignSelf(ui.Center)
}

// fs is a type size in steps from the theme's own, so the window follows
// the desktop's text size setting.
func fs(c *ui.Context, delta float32) float32 { return c.Theme().FontSize + delta }

// header says what the window is, and holds the actions that are not about
// one particular server.
func (a *guiApp) header(c *ui.Context) {
	t := c.Theme()

	paneRow(c).Padding(16, 22, 14, 22).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(32, 32).Radius(9).Background(t.Accent.Alpha(0.12)).Center().Shrink(0).Children(func() {
			ui.Text(c, "CS").FontSize(fs(c, -3)).Bold().TextColor(t.Accent).LetterSpacing(0.3)
		})

		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, "Claude Science").FontSize(fs(c, 2)).Bold().SingleLine()
			ui.Text(c, "连接一台服务器，在浏览器里开始使用").FontSize(fs(c, -2)).TextColor(t.TextMuted).SingleLine()
		})

		more := ui.ButtonBase(c).Padding(6).Radius(7).Label("更多操作").Tooltip("更多操作")
		more.Children(func() {
			ui.Icon(c, moreIcon).Size(17, 17).TextColor(t.TextMuted)
		})
		if more.Hovered() {
			more.Background(t.SurfaceHover)
		}
		more.PopoverMenuButton(func(m *ui.Menu) {
			if m.Item("重新加载配置").Shortcut(ui.Cmd, ui.KeyR).Chosen() {
				a.reload()
			}
			if m.Item("打开配置文件夹").Shortcut(ui.Cmd, ui.KeyO).Chosen() {
				a.openConfigDir()
			}
			m.Separator()
			if m.Item("退出").Shortcut(ui.Cmd, ui.KeyQ).Chosen() {
				mygo.App.Quit()
			}
		}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd})
	})

	ui.Divider(c)
}

// card shows one server: its identity, the state of its tunnel, and the
// action that state calls for. An idle card is two lines; a card that is
// doing something grows a state line.
func (a *guiApp) card(c *ui.Context, i int, it *targetUI) {
	t := c.Theme()

	// A whole idle card is the target's hit area, so the row can be clicked
	// anywhere the pointer rests; the button inside takes its own clicks
	// first, and stays the keyboard path to the same action.
	card := ui.Row(c).Padding(13, 16).Gap(13).AlignItems(ui.Start).Radius(10).
		Background(t.Surface).Border(1, t.Border)
	if it.phase == phaseIdle {
		card.Cursor(ui.CursorPointer)
	}
	if card.Hovered() {
		card.Border(1, t.Border.Mix(t.Text, 0.12))
	}
	if it.phase == phaseIdle && card.Clicked() {
		a.open(i)
	}

	card.Children(func() {
		// The dot sits on the name's line, at the cap height of its text.
		ui.Box(c).Shrink(0).Width(9).Height(fs(c, 6)).Center().Children(func() {
			a.statusDot(c, it)
		})

		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, displayName(it.target)).FontSize(fs(c, 0)).Bold().SingleLine().Ellipsis("…")
			ui.Text(c, address(it.target)).FontSize(fs(c, -2)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			if line := statusLine(it); line != "" {
				ui.Text(c, line).FontSize(fs(c, -2)).TextColor(statusColor(c, it)).Wrap().MaxLines(2)
			}
			a.detailToggle(c, it)
		})

		ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			a.actions(c, i, it)
		})
	})
}

// statusDot is the state of the tunnel in one glance: quiet while idle,
// spinning while connecting, green when up, red when it failed. The words
// beside it name the state, so the dot is decoration to a screen reader.
func (a *guiApp) statusDot(c *ui.Context, it *targetUI) {
	t := c.Theme()

	switch it.phase {
	case phaseConnecting:
		ui.Spinner(c).Shrink(0).AccessHidden(true)
	case phaseConnected:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Success).Shrink(0).AccessHidden(true)
	case phaseError:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Danger).Shrink(0).AccessHidden(true)
	default:
		ui.Box(c).Size(8, 8).Radius(999).Background(t.Border).Border(1, t.TextMuted.Alpha(0.35)).Shrink(0).AccessHidden(true)
	}
}

// actions builds a card's buttons, and runs what they ask for.
func (a *guiApp) actions(c *ui.Context, i int, it *targetUI) {
	switch it.phase {
	case phaseConnected:
		if it.sess != nil && it.sess.URL() != "" {
			if actionButton(c, "打开").Key(fmt.Sprintf("open-%d", i)).Clicked() {
				a.open(i)
			}
		}
		if ghostButton(c, "断开").Key(fmt.Sprintf("stop-%d", i)).Clicked() {
			a.close(i)
		}
	case phaseConnecting, phaseError:
		if ghostButton(c, "停止").Key(fmt.Sprintf("stop-%d", i)).Clicked() {
			a.close(i)
		}
	default:
		if actionButton(c, "连接").Key(fmt.Sprintf("connect-%d", i)).Clicked() {
			a.open(i)
		}
	}
}

// detailToggle reveals the technical detail of a card, on request only.
func (a *guiApp) detailToggle(c *ui.Context, it *targetUI) {
	if it.detail == "" {
		return
	}
	t := c.Theme()

	toggle := ui.ButtonBase(c).Padding(3, 4).MarginStart(-4).Radius(5).
		TextColor(t.TextMuted).AlignSelf(ui.Start).
		Tooltip("显示或隐藏原始错误信息")
	toggle.Children(func() {
		ui.Text(c, "详细信息").FontSize(fs(c, -3))
		icon := ui.Icon(c, chevron).Size(12, 12).TextColor(t.TextMuted).Shrink(0)
		if !it.showDetail {
			icon.Rotate(-90)
		}
	})
	if toggle.Hovered() {
		toggle.Background(t.SurfaceHover)
	}
	if toggle.Clicked() {
		it.showDetail = !it.showDetail
	}

	if it.showDetail {
		ui.Text(c, it.detail).FontSize(fs(c, -3)).Font("monospace").LineHeight(1.5).
			TextColor(t.TextMuted).Wrap().Selectable().
			Padding(6, 8).Radius(6).Background(t.SurfaceHover).
			AlignSelf(ui.Stretch).Margin(2, 0)
	}
}

// footer counts the servers and names the shortcuts that are not in a menu.
func (a *guiApp) footer(c *ui.Context) {
	t := c.Theme()

	ui.Divider(c)
	paneRow(c).Padding(9, 22).Gap(14).AlignItems(ui.Center).Children(func() {
		ui.Text(c, a.summary()).FontSize(fs(c, -3)).
			TextColor(t.TextMuted).Grow(1).SingleLine().Ellipsis("…")
		ui.Text(c, keys("Cmd+R")+" 重新加载").FontSize(fs(c, -3)).TextColor(t.TextMuted).SingleLine()
	})
}

// summary counts the servers and the tunnels that are up, so the window
// answers "is anything running?" without reading the cards.
func (a *guiApp) summary() string {
	live := 0
	for _, it := range a.items {
		if it.phase == phaseConnected {
			live++
		}
	}
	if live == 0 {
		return fmt.Sprintf("%d 台服务器", len(a.items))
	}
	return fmt.Sprintf("%d 台服务器 · %d 台已连接", len(a.items), live)
}

// emptyState says what to do when the config lists no server, without asking
// the reader to picture JSON.
func (a *guiApp) emptyState(c *ui.Context) {
	t := c.Theme()

	ui.Column(c).Fill().Center().Padding(40, 32).Gap(8).Children(func() {
		ui.Box(c).Size(40, 40).Radius(11).Background(t.Surface).Border(1, t.Border).Center().Children(func() {
			ui.Icon(c, plusIcon).Size(18, 18).TextColor(t.TextMuted)
		})
		ui.Text(c, "还没有服务器").FontSize(fs(c, 1)).Bold().Margin(4, 0, 0, 0)
		ui.Text(c, "把一台服务器加进配置文件，保存后回到这里重新加载。").
			FontSize(fs(c, -2)).TextColor(t.TextMuted).Wrap().MaxWidth(360).TextAlign(ui.Center)
		ui.Row(c).Gap(6).Margin(6, 0, 0, 0).Children(func() {
			if actionButton(c, "打开配置文件夹").Clicked() {
				a.openConfigDir()
			}
			if ghostButton(c, "重新加载").Clicked() {
				a.reload()
			}
		})
	})
}

// brokenState is a config error: what happened in one sentence, the ways
// out, and the parser's own text only when asked for.
func (a *guiApp) brokenState(c *ui.Context) {
	t := c.Theme()

	ui.Column(c).Fill().Center().Padding(40, 32).Gap(8).Children(func() {
		ui.Box(c).Size(40, 40).Radius(11).Background(t.Danger.Alpha(0.12)).Center().Children(func() {
			ui.Icon(c, alertIcon).Size(20, 20).TextColor(t.Danger)
		})
		ui.Text(c, "无法读取配置").FontSize(fs(c, 1)).Bold().Margin(4, 0, 0, 0)
		ui.Text(c, "请确认配置文件存在、格式正确。改好后点重试即可。").
			FontSize(fs(c, -2)).TextColor(t.TextMuted).Wrap().MaxWidth(360).TextAlign(ui.Center)
		ui.Row(c).Gap(6).Margin(6, 0, 0, 0).Children(func() {
			if actionButton(c, "重试").Clicked() {
				a.reload()
			}
			if ghostButton(c, "打开配置文件夹").Clicked() {
				a.openConfigDir()
			}
		})

		toggle := ui.ButtonBase(c).Padding(4, 6).Radius(5).TextColor(t.TextMuted).
			Margin(2, 0, 0, 0).Tooltip("显示或隐藏原始错误信息")
		toggle.Children(func() { ui.Text(c, "详细信息").FontSize(fs(c, -3)) })
		if toggle.Hovered() {
			toggle.Background(t.SurfaceHover)
		}
		if toggle.Clicked() {
			a.showErr = !a.showErr
		}
		if a.showErr {
			ui.Text(c, a.err.Error()).FontSize(fs(c, -3)).Font("monospace").LineHeight(1.5).
				TextColor(t.TextMuted).Wrap().Selectable().MaxWidth(420).
				Padding(8, 10).Radius(8).Background(t.Surface).Border(1, t.Border)
		}
	})
}

// actionButton is the one button that matters on a card: the accent fill.
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

// statusColor carries the meaning of the state into the text of the card.
func statusColor(c *ui.Context, it *targetUI) ui.Color {
	t := c.Theme()
	switch it.phase {
	case phaseConnected:
		return t.Success
	case phaseError:
		return t.Danger
	}
	return t.TextMuted
}

// statusLine is what the card says about the tunnel in words. An idle card
// says nothing: its button already offers the one thing to do.
func statusLine(it *targetUI) string {
	if it.phase == phaseIdle {
		return ""
	}
	return it.status
}

// displayName is the target's name, or where it points when it has none.
func displayName(target Target) string {
	if strings.TrimSpace(target.Name) != "" {
		return target.Name
	}
	if target.SSHHost == "" {
		return "未命名服务器"
	}
	return target.SSHHost
}

// address is the identity of a target: who to connect as, and where. Ports,
// keys, containers and commands stay out of the window.
func address(target Target) string {
	host := target.SSHHost
	if host == "" {
		return "未配置地址"
	}
	if target.SSHUser == "" {
		return host
	}
	return target.SSHUser + "@" + host
}

// keys spells a shortcut as the platform does: ⌘R on macOS, Ctrl+R
// elsewhere.
func keys(shortcut string) string {
	if runtime.GOOS == "darwin" {
		return strings.NewReplacer("Cmd+", "⌘", "Ctrl+", "⌃", "Alt+", "⌥", "Shift+", "⇧").Replace(shortcut)
	}
	return strings.ReplaceAll(shortcut, "Cmd+", "Ctrl+")
}

// open starts the target's session, or reopens its login page when the
// session is already running.
func (a *guiApp) open(i int) {
	it := a.items[i]
	if it.sess != nil {
		if url := it.sess.URL(); url != "" {
			openBrowser(url)
			return
		}
		// The first attempt never found a login URL; start over so the
		// command runs again.
		it.sess.Stop()
		it.sess = nil
	}

	s := newSession(it.target)
	it.sess = s
	it.phase = phaseConnecting
	it.status = "正在连接"
	it.detail = ""
	it.showDetail = false

	s.Start(func(phase sessionPhase, status, detail string) {
		if a.win == nil {
			return
		}
		a.win.Update(func() { a.report(it, s, phase, status, detail) })
	})
}

// report writes a session's state to its card, on the main thread. A card
// that has moved on to another session ignores what the old one says.
func (a *guiApp) report(it *targetUI, s *session, phase sessionPhase, status, detail string) {
	if it.sess != s {
		return
	}
	it.phase = phase
	it.status = status
	switch {
	case detail != "":
		it.detail = detail
		// A new failure folds the detail away again, so the card stays
		// quiet unless the reader asks for it.
		if phase == phaseError {
			it.showDetail = false
		}
	case phase == phaseConnected || phase == phaseIdle:
		it.detail = ""
		it.showDetail = false
	}
}

func (a *guiApp) close(i int) {
	it := a.items[i]
	if it.sess == nil {
		return
	}
	it.sess.Stop()
	it.sess = nil
	it.phase = phaseIdle
	it.status = ""
	it.detail = ""
	it.showDetail = false
}

// openConfigDir opens the folder that holds the config file: the one place
// the launcher points at the file itself.
func (a *guiApp) openConfigDir() {
	dir := filepath.Dir(a.configPath)
	if dir == "" {
		dir = "."
	}
	if err := mygo.Shell.OpenPath(dir); err != nil {
		log.Printf("opening config directory: %v", err)
	}
}
