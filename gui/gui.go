// Package gui is the desktop window of claude-science-container: two tabs —
// remote forwarding and a local Claude Science — editing the same config.json
// the CLI reads. The view layer is headless-testable (see the _test files):
// every state renders with ui.NewTester, without a display.
package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// App is the window's state, read and changed on the main thread only.
type App struct {
	win        *mygo.Window
	configPath string

	cfg *config.Config
	err error

	tab int

	targets []*forwardCard
	local   *localState

	bannerText   string
	bannerDetail string
	showBanner   bool

	showErr bool
}

var (
	moreIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.9"/><circle cx="12" cy="12" r="1.9"/><circle cx="19" cy="12" r="1.9"/></svg>`))
	chevron   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>`))
	alertIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 8v5"/><circle cx="12" cy="16.6" r="1.1" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="9"/></svg>`))
	plusIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>`))
)

// Run opens the window on configPath.
func Run(configPath string) error {
	app := New(configPath)
	app.reload()

	mygo.App.WhenReady(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Claude Science",
			Width:     760,
			Height:    640,
			MinWidth:  560,
			MinHeight: 440,
			Content:   ui.View(app.view),
		})
	})
	return mygo.App.Run()
}

// New builds an app without a window, which is what the tests use.
func New(configPath string) *App {
	return &App{configPath: configPath}
}

func (a *App) view(c *ui.Context) {
	c.SetTheme(adaptedTheme(c, c.Theme().Dark))

	c.OnShortcut(ui.Cmd, ui.KeyS, func() { a.save() })
	c.OnShortcut(ui.Cmd, ui.KeyR, a.reload)
	c.OnShortcut(ui.Cmd, ui.KeyO, a.openConfigDir)
	c.OnShortcut(ui.Cmd, ui.KeyQ, mygo.App.Quit)

	ui.Column(c).Fill().Children(func() {
		a.header(c)
		a.tabBar(c)

		if a.err != nil {
			a.brokenState(c)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				pane(c).Gap(12).Padding(16, 22, 18, 22).Children(func() {
					if a.bannerText != "" {
						banner(c, a.bannerText, a.bannerDetail, &a.showBanner)
					}
					switch a.tab {
					case 1:
						a.localView(c)
					default:
						a.forwardView(c)
					}
				})
			})
			a.footer(c)
		}
	})
}

// header says what the window is, and holds the actions that are not about
// one particular server: save, reload, open the config folder, quit.
func (a *App) header(c *ui.Context) {
	t := c.Theme()

	paneRow(c).Padding(16, 22, 12, 22).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(32, 32).Radius(9).Background(t.Accent.Alpha(0.12)).Center().Shrink(0).Children(func() {
			ui.Text(c, "CS").FontSize(fs(c, -3)).Bold().TextColor(t.Accent).LetterSpacing(0.3)
		})

		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, "Claude Science").FontSize(fs(c, 2)).Bold().SingleLine()
			ui.Text(c, "转发远程实例，或在本机用第三方 API 启动").
				FontSize(fs(c, -2)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})

		var save ui.Element
		if a.dirty() {
			save = actionButton(c, "保存").Key("save").Tooltip("保存到 config.json（" + keys("Cmd+S") + "）")
		} else {
			save = ghostButton(c, "已保存").Key("save").Tooltip("配置已写入 config.json")
		}
		if save.Clicked() {
			a.save()
		}

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

func (a *App) tabBar(c *ui.Context) {
	paneRow(c).Padding(0, 18, 0, 18).Children(func() {
		ui.Tabs(c, &a.tab, "转发", "启动本地的 Claude Science")
	})
	ui.Divider(c)
}

// footer names the file the window edits and whether the draft is saved,
// without putting the path itself in the surface.
func (a *App) footer(c *ui.Context) {
	t := c.Theme()

	ui.Divider(c)
	paneRow(c).Padding(9, 22).Gap(14).AlignItems(ui.Center).Children(func() {
		status := "配置已保存"
		if d := a.dirty(); d {
			status = "有未保存的修改"
		}
		ui.Text(c, status).FontSize(fs(c, -3)).
			TextColor(t.TextMuted).Grow(1).SingleLine().Ellipsis("…")
		ui.Text(c, keys("Cmd+S")+" 保存  ·  "+keys("Cmd+R")+" 重新加载").
			FontSize(fs(c, -3)).TextColor(t.TextMuted).SingleLine()
	})
}

// brokenState is a config error: what happened in one sentence, the ways out,
// and the parser's own text only when asked for.
func (a *App) brokenState(c *ui.Context) {
	t := c.Theme()

	ui.Column(c).Fill().Center().Padding(40, 32).Gap(8).Children(func() {
		ui.Box(c).Size(40, 40).Radius(11).Background(t.Danger.Alpha(0.12)).Center().Children(func() {
			ui.Icon(c, alertIcon).Size(20, 20).TextColor(t.Danger)
		})
		ui.Text(c, "无法读取配置").FontSize(fs(c, 1)).Bold().Margin(4, 0, 0, 0)
		ui.Text(c, "请确认配置文件存在、格式正确。改好后点重试即可。").
			FontSize(fs(c, -2)).TextColor(t.TextMuted).Wrap().MaxWidth(360).TextAlign(ui.Center)
		ui.Row(c).Gap(6).Margin(6, 0, 0, 0).Children(func() {
			if actionButton(c, "重试").Key("retry").Clicked() {
				a.reload()
			}
			if ghostButton(c, "打开配置文件夹").Key("open-dir").Clicked() {
				a.openConfigDir()
			}
		})

		ui.Column(c).WidthPercent(100).MaxWidth(560).AlignSelf(ui.Center).Children(func() {
			a.showErr = detailToggle(c, "error-detail", a.err.Error(), a.showErr)
		})
	})
}

// save writes the draft to config.json and keeps the sessions whose target did
// not change. It returns whether the file now matches what the window shows.
func (a *App) save() bool {
	if a.cfg == nil || a.err != nil {
		return false
	}
	cand, problems := a.candidate()
	if len(problems) > 0 {
		a.bannerText = "配置有误，未保存"
		a.bannerDetail = strings.Join(problems, "\n")
		a.showBanner = false
		return false
	}
	if err := config.Save(a.configPath, cand); err != nil {
		a.bannerText = "保存失败"
		a.bannerDetail = err.Error()
		a.showBanner = false
		return false
	}

	a.cfg = cand
	for i, item := range a.targets {
		if i < len(cand.Forward.Targets) {
			item.commit(cand.Forward.Targets[i])
		}
	}
	a.bannerText = ""
	a.bannerDetail = ""
	return true
}

// reload reads the config file again. A target whose settings did not change
// keeps its session, so saving one card does not drop another card's tunnel.
func (a *App) reload() {
	cfg, _, err := config.LoadOrCreate(a.configPath)
	if err != nil {
		for _, item := range a.targets {
			item.stop()
		}
		if a.local != nil {
			a.local.stop()
		}
		a.err = err
		a.targets = nil
		a.showErr = false
		return
	}

	a.err = nil
	a.cfg = cfg

	previous := make(map[string]*forwardCard, len(a.targets))
	for _, item := range a.targets {
		previous[targetKey(item.target)] = item
	}

	targets := make([]*forwardCard, 0, len(cfg.Forward.Targets))
	for _, t := range cfg.Forward.Targets {
		if old, ok := previous[targetKey(t)]; ok {
			old.form = forwardFormFrom(t)
			targets = append(targets, old)
			delete(previous, targetKey(t))
			continue
		}
		targets = append(targets, newForwardCard(t))
	}
	for _, item := range previous {
		item.stop()
	}
	a.targets = targets

	// The local stack cannot adopt edited settings, so reloading the file
	// stops it; the next 启动 runs what the file now says.
	if a.local != nil {
		a.local.stop()
	}
	a.local = newLocalState(cfg.Local)
	a.bannerText = ""
	a.bannerDetail = ""
}

func targetKey(t config.Target) string {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Sprintf("%v", t)
	}
	return string(data)
}

// openConfigDir opens the folder that holds the config file: the one place
// the window points at the file itself.
func (a *App) openConfigDir() {
	dir := filepath.Dir(a.configPath)
	if dir == "" {
		dir = "."
	}
	if err := mygo.Shell.OpenPath(dir); err != nil {
		log.Printf("opening config directory: %v", err)
	}
}

// update runs fn on the main thread when there is a window; headless tests
// drive the state directly instead.
func (a *App) update(fn func()) {
	if a.win == nil {
		return
	}
	a.win.Update(fn)
}

func keys(shortcut string) string {
	if runtime.GOOS == "darwin" {
		return strings.NewReplacer("Cmd+", "⌘", "Ctrl+", "⌃", "Alt+", "⌥", "Shift+", "⇧").Replace(shortcut)
	}
	return strings.ReplaceAll(shortcut, "Cmd+", "Ctrl+")
}
