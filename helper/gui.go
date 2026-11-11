package main

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// targetUI is one button in the window: a configured target and the
// session opened from it, if any.
type targetUI struct {
	target Target
	sess   *session

	status string
	detail string
	failed bool
}

type guiApp struct {
	win        *mygo.Window
	configPath string
	items      []*targetUI
	err        string
}

func runGUI(configPath string) {
	app := &guiApp{configPath: configPath}
	app.reload()

	mygo.App.WhenReady(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Claude Science 启动器",
			Width:     560,
			Height:    460,
			MinWidth:  420,
			MinHeight: 320,
			Content:   ui.View(app.view),
		})
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatalf("starting the GUI: %v (use --cli for the terminal launcher)", err)
	}
}

// reload reads the config file again, stopping any session whose button no
// longer belongs to the loaded config.
func (a *guiApp) reload() {
	for _, it := range a.items {
		if it.sess != nil {
			it.sess.Stop()
		}
	}

	targets, err := LoadConfig(a.configPath)
	if err != nil {
		a.err = err.Error()
		a.items = nil
		return
	}

	a.err = ""
	a.items = make([]*targetUI, 0, len(targets))
	for _, target := range targets {
		a.items = append(a.items, &targetUI{target: target, status: "未启动"})
	}
}

func (a *guiApp) view(c *ui.Context) {
	t := c.Theme()

	ui.Column(c).Fill().Padding(22).Gap(12).Children(func() {
		ui.Text(c, "Claude Science").FontSize(20).Bold()

		if a.err != "" {
			ui.Text(c, "读取配置失败").FontSize(14).Bold().TextColor(t.Danger)
			ui.Text(c, a.err).Wrap().TextColor(t.Danger)
			ui.Text(c, a.configPath).FontSize(11).Wrap().TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "重试").Clicked() {
					a.reload()
				}
				if ui.Button(c, "打开配置目录").Clicked() {
					if err := mygo.Shell.OpenPath(filepath.Dir(a.configPath)); err != nil {
						log.Printf("opening config directory: %v", err)
					}
				}
			})
			return
		}

		if len(a.items) == 0 {
			ui.Text(c, "配置里没有可用的目标").TextColor(t.TextMuted)
			return
		}

		ui.Scroll(c).Grow(1).Gap(12).Children(func() {
			for i, it := range a.items {
				a.targetView(c, i, it)
			}
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.configPath).FontSize(10).Grow(1).TextColor(t.TextMuted)
			if ui.Button(c, "重新加载").Clicked() {
				a.reload()
			}
			if ui.Button(c, "退出").Clicked() {
				mygo.App.Quit()
			}
		})
	})
}

func (a *guiApp) targetView(c *ui.Context, i int, it *targetUI) {
	t := c.Theme()

	ui.Column(c).Gap(5).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			label := it.target.Name
			if label == "" {
				label = fmt.Sprintf("%s@%s", it.target.SSHUser, it.target.SSHHost)
			}
			if ui.PrimaryButton(c, label).Key(fmt.Sprintf("open-%d", i)).Grow(1).Clicked() {
				a.open(i)
			}
			if it.sess != nil {
				if ui.Button(c, "断开").Key(fmt.Sprintf("stop-%d", i)).Clicked() {
					a.close(i)
				}
			}
		})

		ui.Text(c, it.status).FontSize(12).TextColor(t.TextMuted)
		if it.detail != "" {
			color := t.TextMuted
			if it.failed {
				color = t.Danger
			}
			ui.Text(c, it.detail).FontSize(11).Wrap().Selectable().TextColor(color)
		}
	})
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
	it.status = "正在启动…"
	it.detail = ""
	it.failed = false

	s.Start(func(status, detail string, failed bool) {
		if a.win == nil {
			return
		}
		a.win.Update(func() {
			if it.sess != s {
				return
			}
			it.status = status
			it.detail = detail
			it.failed = failed
		})
	})
}

func (a *guiApp) close(i int) {
	it := a.items[i]
	if it.sess == nil {
		return
	}
	it.sess.Stop()
	it.sess = nil
	it.status = "已断开"
	it.detail = ""
	it.failed = false
}
