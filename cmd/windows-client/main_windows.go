//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"server-aibot/internal/desktop"
)

const title = "AIBOT 设备客户端"

// Test builds can override this with -ldflags -X to isolate UI verification
// from a user's running client without a runtime singleton bypass.
var instanceNamespace = "AIBOTDesktop"

func main() {
	runtime.LockOSThread()
	background := flag.Bool("background", false, "启动后隐藏到系统托盘")
	flag.Parse()
	if err := run(*background); err != nil {
		walk.MsgBox(nil, title, err.Error(), walk.MsgBoxIconError)
	}
}

// A per-user, per-session mutex prevents duplicate agents. The auto-reset event
// also restores a hidden window without relying on its translated window title.
func singleInstance() (windows.Handle, windows.Handle, bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return 0, 0, false, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return 0, 0, false, err
	}
	name := `Local\` + instanceNamespace + "-" + user.User.Sid.String()
	mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(name))
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, 0, false, err
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		defer windows.CloseHandle(mutex)
		for i := 0; i < 30; i++ {
			event, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name+"-show"))
			if e == nil {
				defer windows.CloseHandle(event)
				return 0, 0, true, windows.SetEvent(event)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return 0, 0, true, fmt.Errorf("客户端正在启动，请稍后再次打开")
	}
	event, err := windows.CreateEvent(nil, 0, 0, windows.StringToUTF16Ptr(name+"-show"))
	if err != nil {
		windows.CloseHandle(mutex)
		return 0, 0, false, err
	}
	return mutex, event, false, nil
}

func run(background bool) error {
	mutex, event, existing, err := singleInstance()
	if err != nil || existing {
		return err
	}
	defer windows.CloseHandle(mutex)
	defer windows.CloseHandle(event)
	manager, err := desktop.New()
	if err != nil {
		return fmt.Errorf("无法初始化客户端：%w", err)
	}
	defer manager.Close()
	cfg, loadErr := manager.Load()
	if cfg.ControlURL == "" {
		cfg.ControlURL = "https://aibot.eugenweb.cn"
	}
	var mw *walk.MainWindow
	var control, id, key, cidrs, ports *walk.LineEdit
	var auto, login, followLogs *walk.CheckBox
	var status, connectionHint *walk.Label
	var logs *walk.TextEdit
	var save, connect, disconnect, revealKey, openConsole *walk.PushButton
	var trayConnect, trayDisconnect *walk.Action
	var preset *walk.ComboBox
	presets := []struct{ name, cidrs, ports string }{
		{"自定义", "", ""},
		{"默认：内网，禁止回环", "", ""},
		{"全部放开", "*", "*"},
		{"内网 + 本机 Orca（6768）", "", "6768"},
		{"内网 + 本机远程管理（SSH / RDP）", "", "22,3389"},
		{"内网 + 本机 Web 开发", "", "3000,5173,6768,8000,8080"},
	}
	presetNames := make([]string, len(presets))
	for i, p := range presets {
		presetNames[i] = p.name
	}
	var applyingPreset bool
	customize := func() {
		if preset != nil && !applyingPreset {
			preset.SetCurrentIndex(0)
		}
	}
	var busy, quitting bool
	var operations sync.WaitGroup
	savedKey := cfg.DeviceKey
	redact := func(s string) string {
		for _, secret := range []string{savedKey, key.Text()} {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, "[已隐藏]")
			}
		}
		return s
	}
	hideKey := func() {
		key.SetPasswordMode(true)
		revealKey.SetText("显示密钥")
	}
	show := func() { mw.Show(); win.ShowWindow(mw.Handle(), win.SW_RESTORE); win.SetForegroundWindow(mw.Handle()) }
	showError := func(e error) {
		if e != nil {
			show()
			walk.MsgBox(mw, "操作失败", redact(e.Error()), walk.MsgBoxIconError)
		}
	}
	refresh := func() {
		s := manager.Status()
		state := "未连接"
		if s.Running {
			state = "正在连接"
		}
		if s.RetryAttempt > 0 {
			state = "等待重连"
			if s.NextRetryAt.IsZero() {
				state = "正在重连"
			}
		}
		if s.Connected {
			state = "已连接"
		}
		statusText := "状态：" + state + "  " + redact(s.Detail)
		status.SetText("● " + state)
		status.SetToolTipText(statusText)
		hint := "填写设备信息后，点击“保存并连接”。"
		color := walk.RGB(110, 115, 128)
		if s.Running {
			hint = s.Detail
			color = walk.RGB(0, 122, 255)
		}
		if s.Connected {
			hint = "设备已在线，关闭窗口仍保持连接。"
			color = walk.RGB(30, 140, 80)
		}
		status.SetTextColor(color)
		connectionHint.SetText(hint)
		text := strings.ReplaceAll(strings.ReplaceAll(redact(manager.Logs()), "\r\n", "\n"), "\n", "\r\n")
		if followLogs.Checked() && logs.Text() != text {
			logs.SetText(text)
			logs.SetTextSelection(len([]rune(text)), len([]rune(text)))
		}
		for _, input := range []*walk.LineEdit{control, id, key, cidrs, ports} {
			input.SetReadOnly(busy || s.Running)
		}
		preset.SetEnabled(!busy && !s.Running)
		auto.SetEnabled(!busy && !s.Running)
		login.SetEnabled(!busy && !s.Running)
		save.SetEnabled(!busy && !s.Running)
		connect.SetEnabled(!busy && !s.Running)
		disconnect.SetEnabled(!busy && s.Running)
		if trayConnect != nil {
			trayConnect.SetEnabled(!busy && !s.Running)
			trayDisconnect.SetEnabled(!busy && s.Running)
		}
	}
	perform := func(work func() error, after func()) {
		if busy || quitting {
			return
		}
		busy = true
		refresh()
		operations.Add(1)
		go func() {
			e := work()
			operations.Done()
			mw.Synchronize(func() {
				if quitting {
					return
				}
				busy = false
				if after != nil && e == nil {
					after()
				}
				refresh()
				showError(e)
			})
		}()
	}
	start := func() { hideKey(); perform(manager.Start, nil) }
	saveConfig := func(andConnect bool) {
		next := desktop.Config{ControlURL: strings.TrimSpace(control.Text()), DeviceID: strings.TrimSpace(id.Text()), DeviceKey: strings.TrimSpace(key.Text()), AllowCIDRs: strings.TrimSpace(cidrs.Text()), LoopbackPorts: strings.TrimSpace(ports.Text()), AutoConnect: auto.Checked(), StartAtLogin: login.Checked()}
		hideKey()
		perform(func() error { return manager.Save(next) }, func() {
			savedKey = next.DeviceKey
			key.SetText(next.DeviceKey)
			if andConnect {
				start()
			} else {
				walk.MsgBox(mw, title, "配置已保存，尚未连接。请点击“保存并连接”启动连接。", walk.MsgBoxIconInformation)
			}
		})
	}
	connectCurrent := func() { saveConfig(true) }
	stop := func() { perform(func() error { manager.Stop(); return nil }, nil) }
	openControlPage := func() {
		u, e := url.Parse(strings.TrimSpace(control.Text()))
		if e != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			showError(fmt.Errorf("控制 URL 必须是有效的 HTTP 或 HTTPS 地址"))
			return
		}
		showError(exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u.String()).Start())
	}
	// Keep native controls (including keyboard navigation and secret masking),
	// using grouped surfaces and a restrained accent to echo iOS Settings.
	pageColor := walk.RGB(242, 242, 247)
	cardColor := walk.RGB(255, 255, 255)
	accentColor := walk.RGB(0, 102, 204)
	secondaryColor := walk.RGB(99, 99, 108)
	var cardRefs []**walk.Composite
	section := func(name string, children ...Widget) Widget {
		var card *walk.Composite
		cardRefs = append(cardRefs, &card)
		return Composite{
			AssignTo:   &card,
			Background: SolidColorBrush{Color: cardColor},
			Layout:     VBox{Alignment: AlignHNearVNear, Margins: Margins{Left: 18, Top: 14, Right: 18, Bottom: 14}, Spacing: 10},
			Children: append([]Widget{
				Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
					Label{Text: name, TextColor: accentColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 10, Bold: true}}, HSpacer{},
				}},
			}, children...),
		}
	}
	err = (MainWindow{
		AssignTo: &mw, Title: title, Visible: false, MinSize: Size{Width: 460, Height: 540}, MaxSize: Size{Width: 460}, Size: Size{Width: 460, Height: 700},
		Background: SolidColorBrush{Color: pageColor},
		Font:       Font{Family: "Microsoft YaHei UI", PointSize: 10}, Layout: VBox{Alignment: AlignHNearVNear, Margins: Margins{Left: 22, Top: 18, Right: 22, Bottom: 16}, Spacing: 12},
		Children: []Widget{
			Composite{Layout: VBox{Alignment: AlignHNearVNear, MarginsZero: true, Spacing: 4}, Children: []Widget{
				Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
					Label{Text: "AIBOT", TextColor: walk.RGB(28, 28, 30), Font: Font{Family: "Microsoft YaHei UI", PointSize: 22, Bold: true}}, HSpacer{},
				}},
				Label{Text: "设备连接与访问管理", TextColor: secondaryColor},
				Label{AssignTo: &status, Text: "● 未连接", TextColor: accentColor, NoPrefix: true, EllipsisMode: EllipsisEnd},
				Label{AssignTo: &connectionHint, Text: "填写设备信息后，点击“保存并连接”。", TextColor: secondaryColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}},
			}},
			ScrollView{HorizontalFixed: false, StretchFactor: 1, Background: SolidColorBrush{Color: pageColor}, Layout: VBox{Alignment: AlignHNearVNear, Margins: Margins{Right: 8}, Spacing: 12}, Children: []Widget{
				section("设备与账户",
					Composite{Layout: Grid{Columns: 1, MarginsZero: true, Spacing: 6}, Children: []Widget{
						Label{Text: "控制面地址", ToolTipText: "填写你的控制台网址，例如 https://aibot.eugenweb.cn"}, LineEdit{AssignTo: &control, Text: cfg.ControlURL, Name: "ControlURL", MinSize: Size{Height: 28}},
						Label{Text: "设备 ID"}, LineEdit{AssignTo: &id, Text: cfg.DeviceID, Name: "DeviceID"},
						Label{Text: "设备密钥"}, Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							LineEdit{AssignTo: &key, Text: cfg.DeviceKey, PasswordMode: true, Name: "DeviceKey"},
							PushButton{AssignTo: &revealKey, Text: "显示密钥", OnClicked: func() {
								if key.PasswordMode() {
									key.SetPasswordMode(false)
									revealKey.SetText("隐藏密钥")
								} else {
									hideKey()
								}
							}},
						}},
					}},
					Label{Text: "设备密钥不是管理员登录密码。", TextColor: secondaryColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}},
					LinkLabel{Text: `<a id="console">前往控制台获取设备 ID 和密钥 ↗</a>`, ToolTipText: "在浏览器中打开上方控制面地址，然后进入设备详情查看。", OnLinkActivated: func(_ *walk.LinkLabelLink) { openControlPage() }},
				),
				section("网络访问",
					Composite{Layout: Grid{Columns: 1, MarginsZero: true, Spacing: 6}, Children: []Widget{
						Label{Text: "允许网段（选填）"}, LineEdit{AssignTo: &cidrs, Text: cfg.AllowCIDRs, Name: "AllowCIDRs", CueBanner: "填写 * 允许全部网段；或填写 IP/CIDR", OnTextChanged: customize},
						Label{Text: "回环端口（选填）"}, LineEdit{AssignTo: &ports, Text: cfg.LoopbackPorts, Name: "LoopbackPorts", CueBanner: "填写 * 允许全部端口；或填写 22,3389", OnTextChanged: customize},
						Label{Text: "访问预设"}, ComboBox{AssignTo: &preset, Name: "AccessPreset", Model: presetNames, CurrentIndex: 0, OnCurrentIndexChanged: func() {
							i := preset.CurrentIndex()
							if i <= 0 || i >= len(presets) {
								return
							}
							applyingPreset = true
							defer func() { applyingPreset = false }()
							cidrs.SetText(presets[i].cidrs)
							ports.SetText(presets[i].ports)
						}},
					}},
					Label{Text: "填写 * 可放开全部网段或回环端口。\n更改预设后，保存并连接才会生效。", TextColor: secondaryColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}},
				),
				section("启动偏好",
					CheckBox{AssignTo: &auto, Text: "启动客户端时自动连接", Checked: cfg.AutoConnect},
					CheckBox{AssignTo: &login, Text: "登录 Windows 后自动启动", Checked: cfg.StartAtLogin},
				),
				section("运行日志",
					Label{Text: "时间 · 级别 · 事件\n取消实时更新可暂停阅读，后台仍继续记录。", TextColor: secondaryColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						CheckBox{AssignTo: &followLogs, Text: "实时更新", Checked: true}, HSpacer{},
						PushButton{Text: "清空日志", OnClicked: func() { manager.ClearLogs(); logs.SetText("") }},
					}},
					TextEdit{AssignTo: &logs, ReadOnly: true, VScroll: true, CompactHeight: true, MinSize: Size{Height: 190}, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}, Name: "Logs"},
				),
			}},
			Composite{Background: SolidColorBrush{Color: pageColor}, Layout: VBox{MarginsZero: true, Spacing: 8}, Children: []Widget{
				PushButton{AssignTo: &connect, Text: "保存并连接", MinSize: Size{Height: 40}, Font: Font{Family: "Microsoft YaHei UI", PointSize: 11, Bold: true}, OnClicked: connectCurrent},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 8}, Children: []Widget{
					PushButton{AssignTo: &save, Text: "仅保存配置", MinSize: Size{Height: 32}, OnClicked: func() { saveConfig(false) }},
					PushButton{AssignTo: &disconnect, Text: "断开", OnClicked: stop},
					PushButton{AssignTo: &openConsole, Text: "打开控制台", OnClicked: openControlPage},
				}},
			}},
			Label{Text: "关闭窗口后，可从系统托盘重新打开。", TextColor: secondaryColor, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}},
		},
	}).Create()
	if err != nil {
		return err
	}
	defer mw.Dispose()
	// Keep the portrait width fixed; height remains resizable.
	if !lockAppWidth(mw, 460) {
		return fmt.Errorf("无法固定客户端窗口宽度")
	}
	windowStyle := win.GetWindowLong(mw.Handle(), win.GWL_STYLE)
	win.SetWindowLong(mw.Handle(), win.GWL_STYLE, windowStyle&^win.WS_MAXIMIZEBOX)
	win.SetWindowPos(mw.Handle(), 0, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED)
	for _, card := range cardRefs {
		roundAppCard(*card)
	}
	styleAppButton(connect, true, pageColor)
	for _, button := range []*walk.PushButton{save, disconnect, openConsole} {
		styleAppButton(button, false, pageColor)
	}
	styleAppButton(revealKey, false, cardColor)
	for i, p := range presets {
		if i > 0 && cfg.AllowCIDRs == p.cidrs && cfg.LoopbackPorts == p.ports {
			preset.SetCurrentIndex(i)
			break
		}
	}
	icon, err := walk.NewIconFromResourceId(2)
	if err != nil {
		return fmt.Errorf("加载程序图标：%w", err)
	}
	defer icon.Dispose()
	mw.SetIcon(icon)
	tray, err := walk.NewNotifyIcon(mw)
	if err != nil {
		return err
	}
	defer tray.Dispose()
	if err = tray.SetIcon(icon); err != nil {
		return err
	}
	tray.SetToolTip(title)
	addAction := func(text string, fn func()) (*walk.Action, error) {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(fn)
		return a, tray.ContextMenu().Actions().Add(a)
	}
	if _, err = addAction("显示窗口", show); err != nil {
		return err
	}
	if trayConnect, err = addAction("保存并连接", connectCurrent); err != nil {
		return err
	}
	if trayDisconnect, err = addAction("断开", stop); err != nil {
		return err
	}
	if _, err = addAction("退出", func() {
		if quitting {
			return
		}
		quitting = true
		mw.SetEnabled(false)
		go func() { operations.Wait(); manager.Close(); mw.Synchronize(func() { walk.App().Exit(0) }) }()
	}); err != nil {
		return err
	}
	tray.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			show()
		}
	})
	if err = tray.SetVisible(true); err != nil {
		return err
	}
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !quitting {
			*canceled = true
			hideKey()
			mw.Hide()
		}
	})
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mw.Synchronize(func() {
					if quitting {
						return
					}
					if result, _ := windows.WaitForSingleObject(event, 0); result == windows.WAIT_OBJECT_0 {
						show()
					}
					refresh()
				})
			}
		}
	}()
	refresh()
	if !background || loadErr != nil {
		show()
	}
	if loadErr != nil {
		showError(fmt.Errorf("无法读取配置：%w", loadErr))
	} else if cfg.AutoConnect && cfg.DeviceID != "" && cfg.DeviceKey != "" {
		start()
	}
	mw.Run()
	return nil
}
