//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	styleUser           = windows.NewLazySystemDLL("user32.dll")
	styleGDI            = windows.NewLazySystemDLL("gdi32.dll")
	styleCommon         = windows.NewLazySystemDLL("comctl32.dll")
	createRoundRegion   = styleGDI.NewProc("CreateRoundRectRgn")
	setWindowRegion     = styleUser.NewProc("SetWindowRgn")
	createStyleBrush    = styleGDI.NewProc("CreateSolidBrush")
	fillStyleRect       = styleUser.NewProc("FillRect")
	setStyleSubclass    = styleCommon.NewProc("SetWindowSubclass")
	removeStyleSubclass = styleCommon.NewProc("RemoveWindowSubclass")
	defStyleSubclass    = styleCommon.NewProc("DefSubclassProc")
	appButtons          = map[win.HWND]appButtonStyle{}
	appButtonCallback   uintptr
)

const (
	ownerDrawButton      = 4      // ODT_BUTTON
	ownerDrawNoFocusRect = 0x0200 // ODS_NOFOCUSRECT
)

type appButtonStyle struct {
	button     *walk.PushButton
	primary    bool
	background walk.Color
	parent     win.HWND
}

// Preserve native buttons, focus, keyboard handling and accessibility; only paint changes.
func styleAppButton(button *walk.PushButton, primary bool, background walk.Color) {
	if appButtonCallback == 0 {
		appButtonCallback = syscall.NewCallback(appButtonProc)
	}
	h := button.Handle()
	parent := win.GetParent(h)
	ok, _, _ := setStyleSubclass.Call(uintptr(parent), appButtonCallback, 1, 0)
	if ok == 0 {
		return
	}
	appButtons[h] = appButtonStyle{button, primary, background, parent}
	style := win.GetWindowLong(h, win.GWL_STYLE)
	win.SetWindowLong(h, win.GWL_STYLE, (style&^15)|win.BS_OWNERDRAW)
	button.Invalidate()
}

func appButtonProc(hwnd uintptr, message uint32, wParam, lParam, id, data uintptr) uintptr {
	if message == win.WM_DRAWITEM && lParam != 0 {
		item := (*win.DRAWITEMSTRUCT)(unsafe.Pointer(lParam))
		if style, found := appButtons[item.HwndItem]; found && item.CtlType == ownerDrawButton {
			paintAppButton(item, style)
			return 1
		}
	}
	if message == win.WM_NCDESTROY {
		for h, style := range appButtons {
			if uintptr(style.parent) == hwnd {
				delete(appButtons, h)
			}
		}
		removeStyleSubclass.Call(hwnd, appButtonCallback, id)
	}
	result, _, _ := defStyleSubclass.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func paintAppButton(item *win.DRAWITEMSTRUCT, style appButtonStyle) {
	dc := item.HDC
	saved := win.SaveDC(dc)
	defer win.RestoreDC(dc, saved)
	fill := func(color walk.Color, rounded bool) {
		brush, _, _ := createStyleBrush.Call(uintptr(color))
		if brush == 0 {
			return
		}
		defer win.DeleteObject(win.HGDIOBJ(brush))
		if !rounded {
			fillStyleRect.Call(uintptr(dc), uintptr(unsafe.Pointer(&item.RcItem)), brush)
			return
		}
		win.SelectObject(dc, win.HGDIOBJ(brush))
		win.SelectObject(dc, win.GetStockObject(win.NULL_PEN))
		r := item.RcItem
		radius := int32(18 * style.button.DPI() / 96)
		win.RoundRect(dc, r.Left, r.Top, r.Right, r.Bottom, radius, radius)
		win.SelectObject(dc, win.GetStockObject(win.WHITE_BRUSH))
	}
	fill(style.background, false)
	color, ink := walk.RGB(233, 234, 240), walk.RGB(40, 43, 53)
	if style.primary {
		color, ink = walk.RGB(0, 122, 255), walk.RGB(255, 255, 255)
	}
	if item.ItemState&win.ODS_SELECTED != 0 {
		if style.primary {
			color = walk.RGB(0, 95, 211)
		} else {
			color = walk.RGB(214, 219, 229)
		}
	}
	if item.ItemState&win.ODS_DISABLED != 0 || !style.button.Enabled() {
		color, ink = walk.RGB(231, 233, 239), walk.RGB(153, 158, 170)
	}
	fill(color, true)
	font := win.SendMessage(item.HwndItem, win.WM_GETFONT, 0, 0)
	if font != 0 {
		win.SelectObject(dc, win.HGDIOBJ(font))
	}
	win.SetBkMode(dc, win.TRANSPARENT)
	win.SetTextColor(dc, win.COLORREF(ink))
	text, _ := syscall.UTF16PtrFromString(style.button.Text())
	bounds := item.RcItem
	win.DrawTextEx(dc, text, -1, &bounds, win.DT_CENTER|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_NOPREFIX, nil)
	if item.ItemState&win.ODS_FOCUS != 0 && item.ItemState&ownerDrawNoFocusRect == 0 {
		inset := int32(5 * style.button.DPI() / 96)
		bounds.Left += inset
		bounds.Top += inset
		bounds.Right -= inset
		bounds.Bottom -= inset
		win.DrawFocusRect(dc, &bounds)
	}
}

var fixedWidthCallback uintptr
var windowDPI = styleUser.NewProc("GetDpiForWindow")

func lockAppWidth(window *walk.MainWindow, width int) bool {
	if fixedWidthCallback == 0 {
		fixedWidthCallback = syscall.NewCallback(fixedWidthProc)
	}
	ok, _, _ := setStyleSubclass.Call(uintptr(window.Handle()), fixedWidthCallback, 2, uintptr(width))
	return ok != 0
}

func fixedWidthProc(hwnd uintptr, message uint32, wParam, lParam, id, width uintptr) uintptr {
	if message == win.WM_SYSCOMMAND && wParam&0xfff0 == win.SC_MAXIMIZE {
		return 0
	}
	if message == win.WM_NCDESTROY {
		removeStyleSubclass.Call(hwnd, fixedWidthCallback, id)
	}
	result, _, _ := defStyleSubclass.Call(hwnd, uintptr(message), wParam, lParam)
	switch message {
	case win.WM_GETMINMAXINFO:
		if lParam != 0 {
			dpi, _, _ := windowDPI.Call(hwnd)
			if dpi == 0 {
				dpi = 96
			}
			info := (*win.MINMAXINFO)(unsafe.Pointer(lParam))
			pixels := int32((width*dpi + 48) / 96)
			info.PtMinTrackSize.X = pixels
			info.PtMaxTrackSize.X = pixels
		}
	case win.WM_NCHITTEST:
		switch result {
		case win.HTLEFT, win.HTRIGHT:
			return win.HTBORDER
		case win.HTTOPLEFT, win.HTTOPRIGHT:
			return win.HTTOP
		case win.HTBOTTOMLEFT, win.HTBOTTOMRIGHT:
			return win.HTBOTTOM
		}
	}
	return result
}

func roundAppCard(card *walk.Composite) {
	update := func() {
		size := card.SizePixels()
		radius := uintptr(24 * card.DPI() / 96)
		region, _, _ := createRoundRegion.Call(0, 0, uintptr(size.Width+1), uintptr(size.Height+1), radius, radius)
		if region == 0 {
			return
		}
		ok, _, _ := setWindowRegion.Call(uintptr(card.Handle()), region, 1)
		// Windows owns the region only after a successful SetWindowRgn.
		if ok == 0 {
			win.DeleteObject(win.HGDIOBJ(region))
		}
	}
	card.SizeChanged().Attach(update)
	update()
}
