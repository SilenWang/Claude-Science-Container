package gui

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/local"
)

// localState is the 本地启动 tab: the editable local settings and the stack
// they started.
type localState struct {
	form localForm

	runner *local.Runner
	cancel context.CancelFunc

	phase      local.Phase
	detail     string
	url        string
	showDetail bool
}

func newLocalState(l config.Local) *localState {
	return &localState{form: localFormFrom(l), phase: local.PhaseIdle}
}

func (l *localState) kind() phaseKind {
	switch l.phase {
	case local.PhaseReady:
		return phaseOK
	case local.PhaseError:
		return phaseFailed
	case local.PhaseIdle, local.PhaseStopped, "":
		return phaseIdle
	}
	return phaseBusy
}

func (l *localState) statusLine() string {
	switch l.phase {
	case local.PhasePreparing:
		return "正在检查配置…"
	case local.PhaseCredentials:
		return "正在准备本地凭据…"
	case local.PhaseProxy:
		return "正在启动本地代理…"
	case local.PhaseStarting:
		return "正在启动 Claude Science…"
	case local.PhaseReady:
		return "运行中 · 已接管 Anthropic 接口"
	case local.PhaseError:
		return "启动失败"
	case local.PhaseStopped:
		return "已停止"
	}
	return "未启动"
}

func (l *localState) running() bool {
	switch l.phase {
	case local.PhasePreparing, local.PhaseCredentials, local.PhaseProxy, local.PhaseStarting, local.PhaseReady:
		return true
	}
	return false
}

func (l *localState) stop() {
	if l.runner != nil {
		l.runner.Stop()
		l.runner = nil
	}
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.phase = local.PhaseIdle
	l.detail = ""
	l.url = ""
	l.showDetail = false
}

// webURL is the page to open: the login URL once known, otherwise the local
// address claude-science was told to serve on.
func (l *localState) webURL() string {
	if l.url != "" {
		return l.url
	}
	return fmt.Sprintf("http://127.0.0.1:%s", l.form.csPort)
}

// localView is the 启动本地的 Claude Science tab.
func (a *App) localView(c *ui.Context) {
	f := &a.local.form

	sectionTitle(c, "启动本地的 Claude Science", "用第三方 API 在本机运行 Claude Science")
	a.localStatus(c)

	card(c).Children(func() {
		ui.Text(c, "接口后端").FontSize(fs(c, -1)).Bold()
		label := labelForBackend(f.backend)
		sel := ui.Select(c.Key("backend"), &label, backendLabels)
		if sel.Changed() {
			f.backend = backendFromLabel(label)
		}

		key := f.keys[f.backend]
		field(c, "API Key", func(c *ui.Context) ui.Element {
			return textField(c, "api-key", &key, "sk-…").Password().Label("API Key")
		})
		base := f.bases[f.backend]
		field(c, "Base URL", func(c *ui.Context) ui.Element {
			return textField(c, "base-url", &base, "https://api.deepseek.com/v1").Label("Base URL")
		})
		f.keys[f.backend] = key
		f.bases[f.backend] = base

		hint(c, "Key 与 Base URL 按后端分别保存；切回该后端时会恢复。留空时沿用同名环境变量。")
	})

	card(c).Children(func() {
		ui.Text(c, "模型与代理").FontSize(fs(c, -1)).Bold()
		fieldPair(c, "强制模型",
			func(c *ui.Context) ui.Element {
				return textField(c, "force-model", &f.forceModel, "deepseek-flash")
			},
			"代理端口",
			func(c *ui.Context) ui.Element {
				return textField(c, "proxy-port", &f.proxyPort, "9876")
			})
		hint(c, "代理只监听本机回环地址；Claude Science 通过 ANTHROPIC_BASE_URL 指向它。")
	})

	card(c).Children(func() {
		ui.Text(c, "Claude Science").FontSize(fs(c, -1)).Bold()
		fieldPair(c, "可执行文件路径",
			func(c *ui.Context) ui.Element {
				return textField(c, "cs-path", &f.csPath, "claude-science")
			},
			"Web 端口",
			func(c *ui.Context) ui.Element {
				return textField(c, "cs-port", &f.csPort, "9981")
			})
		ui.Toggle(c.Key("open-browser"), &f.openBrowser, "启动后自动打开浏览器")
	})

	a.localActions(c)
	hint(c, "启动时会先保存配置，再写入本地凭据并拉起代理与 Claude Science；停止会一并关闭两者。")
}

// localStatus is the running state above the form: whether anything runs,
// the login URL, and the one action that makes sense now.
func (a *App) localStatus(c *ui.Context) {
	t := c.Theme()
	ls := a.local

	card(c).Children(func() {
		ui.Row(c).Gap(11).AlignItems(ui.Start).Children(func() {
			dotBox(c, ls.kind())

			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, ls.statusLine()).FontSize(fs(c, 0)).Bold().SingleLine()
				ui.Text(c, fmt.Sprintf("代理 http://%s:%s  ·  Web http://127.0.0.1:%s",
					defaultIfEmpty(ls.form.proxyHost, "127.0.0.1"), ls.form.proxyPort, ls.form.csPort)).
					FontSize(fs(c, -2)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if ls.phase == local.PhaseReady && ls.url != "" {
					ui.Text(c, ls.url).FontSize(fs(c, -3)).Font("monospace").
						TextColor(t.Success).Wrap().MaxLines(2).Selectable()
				}
			})

			ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
				if ls.running() {
					if ghostButton(c, "打开 Web UI").Key("open-web").Clicked() {
						openURL(ls.webURL())
					}
					if ghostButton(c, "停止").Key("stop-local").Clicked() {
						a.stopLocal()
					}
				} else {
					if actionButton(c, "启动").Key("start-local").Clicked() {
						a.startLocal()
					}
				}
			})
		})

		if ls.detail != "" {
			ls.showDetail = detailToggle(c, "local-detail", ls.detail, ls.showDetail)
		}
	})
}

// localActions repeats the primary action at the end of the form, where the
// reader is after filling it in.
func (a *App) localActions(c *ui.Context) {
	ls := a.local
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ls.running() {
			if ghostButton(c, "停止").Key("stop-local-bottom").Clicked() {
				a.stopLocal()
			}
			if ghostButton(c, "打开 Web UI").Key("open-web-bottom").Clicked() {
				openURL(ls.webURL())
			}
		} else {
			if actionButton(c, "启动本地的 Claude Science").Key("start-local-bottom").Clicked() {
				a.startLocal()
			}
		}
	})
}

// startLocal saves the draft, checks what the runner needs, then brings the
// proxy and claude-science up in the background.
func (a *App) startLocal() {
	ls := a.local
	if ls.running() {
		if ls.url != "" {
			openURL(ls.url)
		}
		return
	}
	if !a.save() {
		return
	}

	effective, ok := a.cfg.Local.EffectiveBackend(a.cfg.Local.DefaultBackend)
	if !ok || strings.TrimSpace(effective.APIKey) == "" {
		a.bannerText = "请先填写 API Key"
		a.bannerDetail = "启动本地的 Claude Science 需要一个第三方后端的 API Key，或设置对应的 *_API_KEY 环境变量。"
		a.showBanner = false
		return
	}
	if strings.TrimSpace(effective.BaseURL) == "" {
		a.bannerText = "请先填写 Base URL"
		a.bannerDetail = "后端需要 OpenAI 兼容的 Base URL，或设置对应的 *_BASE_URL 环境变量。"
		a.showBanner = false
		return
	}

	// A failed attempt leaves its runner behind; stop it before starting over.
	if ls.runner != nil {
		ls.runner.Stop()
		if ls.cancel != nil {
			ls.cancel()
		}
		ls.runner = nil
		ls.cancel = nil
	}

	ls.phase = local.PhasePreparing
	ls.detail = ""
	ls.showDetail = false

	ctx, cancel := context.WithCancel(context.Background())
	ls.cancel = cancel
	var runner *local.Runner
	runner = local.New(local.Options{
		Config:          a.cfg.Local,
		AutoOpenBrowser: a.cfg.Local.OpenBrowser,
		Reporter: func(st local.Status) {
			a.update(func() { a.reportLocal(ls, runner, st) })
		},
	})
	ls.runner = runner

	go func() {
		if err := runner.Start(ctx); err != nil {
			a.update(func() {
				if ls.runner != runner {
					return
				}
				ls.phase = local.PhaseError
				ls.detail = err.Error()
			})
		}
	}()
}

func (a *App) stopLocal() {
	ls := a.local
	if ls.runner == nil {
		return
	}
	runner := ls.runner
	ls.runner = nil
	runner.Stop()
	if ls.cancel != nil {
		ls.cancel()
		ls.cancel = nil
	}
	ls.phase = local.PhaseStopped
	ls.detail = ""
	ls.showDetail = false
}

func (a *App) reportLocal(ls *localState, runner *local.Runner, st local.Status) {
	if ls.runner != runner {
		return
	}
	ls.phase = st.Phase
	if st.URL != "" {
		ls.url = st.URL
	}
	switch st.Phase {
	case local.PhaseError:
		ls.detail = st.Message
		ls.showDetail = false
	case local.PhaseReady:
		ls.detail = ""
	case local.PhaseStopped:
		ls.detail = ""
		ls.url = ""
	}
}

func defaultIfEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
