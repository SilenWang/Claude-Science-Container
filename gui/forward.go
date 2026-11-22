package gui

import (
	"fmt"
	"log"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/forward"
)

// phaseKind is how a card's state reads at a glance.
type phaseKind int

const (
	phaseIdle phaseKind = iota
	phaseBusy
	phaseOK
	phaseFailed
)

// forwardCard is one server: the saved target, the editable copy of it, and
// the tunnel opened from it.
type forwardCard struct {
	target config.Target
	form   forwardForm

	expanded bool

	sess       *forward.Session
	phase      forward.Phase
	detail     string
	url        string
	showDetail bool
}

func newForwardCard(t config.Target) *forwardCard {
	return &forwardCard{target: t, form: forwardFormFrom(t), phase: forward.PhaseIdle}
}

// label names the card in validation messages.
func (f *forwardCard) label() string {
	if strings.TrimSpace(f.form.name) != "" {
		return f.form.name
	}
	if strings.TrimSpace(f.form.host) != "" {
		return f.form.host
	}
	return "未命名服务器"
}

func (f *forwardCard) displayName() string {
	if strings.TrimSpace(f.form.name) != "" {
		return f.form.name
	}
	if strings.TrimSpace(f.form.host) == "" {
		return "未命名服务器"
	}
	return f.form.host
}

func (f *forwardCard) address() string { return joinAddress(f.form.user, f.form.host) }

func (f *forwardCard) kind() phaseKind {
	switch f.phase {
	case forward.PhaseConnected:
		return phaseOK
	case forward.PhaseConnecting:
		return phaseBusy
	case forward.PhaseError:
		return phaseFailed
	}
	return phaseIdle
}

func (f *forwardCard) statusLine() string {
	switch f.phase {
	case forward.PhaseConnecting:
		return "正在连接并获取登录地址…"
	case forward.PhaseConnected:
		return "已连接 · 端口转发中"
	case forward.PhaseError:
		return "连接失败，3 秒后重试"
	case forward.PhaseStopped:
		return "已停止"
	}
	return ""
}

// commit records what was saved, and drops a tunnel whose target changed:
// it can no longer be described by the card.
func (f *forwardCard) commit(t config.Target) {
	if targetKey(t) != targetKey(f.target) && f.sess != nil {
		f.sess.Stop()
		f.sess = nil
		f.phase = forward.PhaseIdle
		f.detail = ""
		f.url = ""
	}
	f.target = t
}

func (f *forwardCard) stop() {
	if f.sess != nil {
		f.sess.Stop()
		f.sess = nil
	}
	f.phase = forward.PhaseIdle
	f.detail = ""
	f.url = ""
	f.showDetail = false
}

// forwardView is the 转发 tab.
func (a *App) forwardView(c *ui.Context) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		sectionTitle(c, "远端服务器", fmt.Sprintf("%d 台 · 通过 SSH 把远程实例映射到本机", len(a.targets)))
		ui.Box(c).Grow(1)
		if ghostButton(c, "添加服务器").Key("add-target").Clicked() {
			a.addTarget()
		}
	})

	if len(a.targets) == 0 {
		a.forwardEmpty(c)
		return
	}

	for i, item := range a.targets {
		a.forwardCardView(c, i, item)
	}

	hint(c, "连接后保持隧道常驻，断线 3 秒后自动重连。端口转发按「本地 → 远端」填写；远端命令输出里的登录地址会自动提取并打开。")
}

func (a *App) forwardEmpty(c *ui.Context) {
	t := c.Theme()

	ui.Column(c).Fill().Center().Padding(36, 28).Gap(8).Children(func() {
		ui.Box(c).Size(40, 40).Radius(11).Background(t.Surface).Border(1, t.Border).Center().Children(func() {
			ui.Icon(c, plusIcon).Size(18, 18).TextColor(t.TextMuted)
		})
		ui.Text(c, "还没有服务器").FontSize(fs(c, 1)).Bold().Margin(4, 0, 0, 0)
		ui.Text(c, "添加一台远端服务器，填好 SSH 地址与密钥，保存后即可连接。").
			FontSize(fs(c, -2)).TextColor(t.TextMuted).Wrap().MaxWidth(380).TextAlign(ui.Center)
		ui.Row(c).Gap(6).Margin(6, 0, 0, 0).Children(func() {
			if actionButton(c, "添加服务器").Key("add-target-empty").Clicked() {
				a.addTarget()
			}
			if ghostButton(c, "打开配置文件夹").Key("open-dir-empty").Clicked() {
				a.openConfigDir()
			}
		})
	})
}

func (a *App) forwardCardView(c *ui.Context, i int, item *forwardCard) {
	t := c.Theme()

	card(c).Children(func() {
		ui.Row(c).Gap(11).AlignItems(ui.Start).Children(func() {
			dotBox(c, item.kind())

			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, item.displayName()).FontSize(fs(c, 0)).Bold().SingleLine().Ellipsis("…")
				ui.Text(c, item.address()).FontSize(fs(c, -2)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if line := item.statusLine(); line != "" {
					ui.Text(c, line).FontSize(fs(c, -2)).TextColor(statusColor(c, item.kind())).Wrap().MaxLines(2)
				}
				if item.phase == forward.PhaseConnected && item.url != "" {
					ui.Text(c, item.url).FontSize(fs(c, -3)).Font("monospace").TextColor(t.TextMuted).
						SingleLine().Ellipsis("…").Selectable()
				}
			})

			ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
				if item.url != "" {
					if ghostButton(c, "打开浏览器").Key(fmt.Sprintf("open-%d", i)).Clicked() {
						openURL(item.url)
					}
				}
				a.forwardActions(c, i, item)
				label := "编辑"
				if item.expanded {
					label = "收起"
				}
				if ghostButton(c, label).Key(fmt.Sprintf("edit-%d", i)).Clicked() {
					item.expanded = !item.expanded
				}
			})
		})

		if item.detail != "" {
			item.showDetail = detailToggle(c, fmt.Sprintf("detail-%d", i), item.detail, item.showDetail)
		}

		if item.expanded {
			ui.Divider(c)
			a.forwardEditor(c, i, item)
		}
	})
}

func (a *App) forwardActions(c *ui.Context, i int, item *forwardCard) {
	switch item.phase {
	case forward.PhaseConnecting:
		if ghostButton(c, "停止").Key(fmt.Sprintf("stop-%d", i)).Clicked() {
			a.disconnect(i)
		}
	case forward.PhaseConnected, forward.PhaseError:
		if ghostButton(c, "断开").Key(fmt.Sprintf("disconnect-%d", i)).Clicked() {
			a.disconnect(i)
		}
	default:
		if actionButton(c, "连接").Key(fmt.Sprintf("connect-%d", i)).Clicked() {
			a.connect(i)
		}
	}
}

// forwardEditor is the card's form: the SSH target, and every port forward.
func (a *App) forwardEditor(c *ui.Context, i int, item *forwardCard) {
	t := c.Theme()
	f := &item.form

	fieldPair(c, "名称",
		func(c *ui.Context) ui.Element { return textField(c, fmt.Sprintf("name-%d", i), &f.name, "production") },
		"SSH 主机",
		func(c *ui.Context) ui.Element {
			return textField(c, fmt.Sprintf("host-%d", i), &f.host, "192.168.1.100")
		})

	fieldPair(c, "SSH 端口",
		func(c *ui.Context) ui.Element { return textField(c, fmt.Sprintf("port-%d", i), &f.port, "22") },
		"SSH 用户",
		func(c *ui.Context) ui.Element { return textField(c, fmt.Sprintf("user-%d", i), &f.user, "root") })

	field(c, "密钥路径", func(c *ui.Context) ui.Element {
		return textField(c, fmt.Sprintf("key-%d", i), &f.key, "~/.ssh/id_ed25519")
	})

	fieldPair(c, "容器 ID（可选）",
		func(c *ui.Context) ui.Element {
			return textField(c, fmt.Sprintf("container-%d", i), &f.container, "claude-science")
		},
		"远端命令",
		func(c *ui.Context) ui.Element {
			return textField(c, fmt.Sprintf("command-%d", i), &f.command, "claude-science url")
		})

	ui.Divider(c)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "端口转发").FontSize(fs(c, -2)).Bold()
		ui.Text(c, "本地端口 → 远端端口").FontSize(fs(c, -3)).TextColor(t.TextMuted)
		ui.Box(c).Grow(1)
		if ghostButton(c, "添加转发").Key(fmt.Sprintf("add-forward-%d", i)).Clicked() {
			f.forwards = append(f.forwards, forwardFormPort{id: f.nextID, local: "9981", remote: "9981"})
			f.nextID++
		}
	})

	if len(f.forwards) == 0 {
		ui.Text(c, "没有端口转发。连接仍会建立，但没有端口映射。").
			FontSize(fs(c, -3)).TextColor(t.TextMuted)
	}
	removed := -1
	for j := range f.forwards {
		pf := &f.forwards[j]
		ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, "本地").FontSize(fs(c, -3)).TextColor(t.TextMuted)
				ui.TextInput(c.Key(fmt.Sprintf("local-%d-%d", i, pf.id)), &pf.local).Placeholder("9981")
			})
			ui.Text(c, "→").FontSize(fs(c, -1)).TextColor(t.TextMuted).Margin(0, 0, 8, 0)
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, "远端").FontSize(fs(c, -3)).TextColor(t.TextMuted)
				ui.TextInput(c.Key(fmt.Sprintf("remote-%d-%d", i, pf.id)), &pf.remote).Placeholder("9981")
			})
			if ghostButton(c, "移除").Key(fmt.Sprintf("remove-forward-%d-%d", i, pf.id)).Clicked() {
				removed = j
			}
		})
	}
	if removed >= 0 {
		f.forwards = append(f.forwards[:removed], f.forwards[removed+1:]...)
	}

	ui.Row(c).Gap(8).Margin(2, 0, 0, 0).Children(func() {
		ui.Box(c).Grow(1)
		if dangerButton(c, "删除这台服务器").Key(fmt.Sprintf("delete-%d", i)).Clicked() {
			a.deleteTarget(i)
		}
	})
}

func (a *App) addTarget() {
	t := config.DefaultTarget()
	t.Name = fmt.Sprintf("服务器 %d", len(a.targets)+1)
	t.SSHHost = ""
	card := newForwardCard(t)
	card.expanded = true
	a.targets = append(a.targets, card)
	a.bannerText = ""
}

func (a *App) deleteTarget(i int) {
	if i < 0 || i >= len(a.targets) {
		return
	}
	a.targets[i].stop()
	a.targets = append(a.targets[:i], a.targets[i+1:]...)
}

// connect saves the draft first, so the tunnel always runs the settings the
// window shows, then starts (or reopens) the card's session.
func (a *App) connect(i int) {
	if i < 0 || i >= len(a.targets) {
		return
	}
	if !a.save() {
		return
	}
	item := a.targets[i]
	if item.sess != nil {
		if item.url != "" {
			openURL(item.url)
			return
		}
		item.sess.Stop()
		item.sess = nil
	}

	sess := forward.NewSession(item.target, forward.Options{AutoOpenBrowser: true})
	item.sess = sess
	item.phase = forward.PhaseConnecting
	item.detail = ""
	item.url = ""
	item.showDetail = false

	sess.Start(func(st forward.Status) {
		a.update(func() { a.reportForward(item, sess, st) })
	})
}

// reportForward lands a session's state on its card. A card that moved on to
// another session ignores what the old one says.
func (a *App) reportForward(item *forwardCard, sess *forward.Session, st forward.Status) {
	if item.sess != sess {
		return
	}
	item.phase = st.Phase
	if st.URL != "" {
		item.url = st.URL
	}
	switch st.Phase {
	case forward.PhaseError:
		item.detail = st.Message
		item.showDetail = false
	case forward.PhaseConnected, forward.PhaseStopped:
		item.detail = ""
		item.showDetail = false
	}
	if st.Phase == forward.PhaseStopped {
		item.phase = forward.PhaseStopped
		item.sess = nil
	}
}

func (a *App) disconnect(i int) {
	if i < 0 || i >= len(a.targets) {
		return
	}
	a.targets[i].stop()
}

func statusColor(c *ui.Context, kind phaseKind) ui.Color {
	t := c.Theme()
	switch kind {
	case phaseOK:
		return t.Success
	case phaseFailed:
		return t.Danger
	}
	return t.TextMuted
}

// openURL opens a login page in the platform's browser.
func openURL(url string) {
	if err := forward.OpenInBrowser(url); err != nil {
		log.Printf("opening browser: %v", err)
	}
}
