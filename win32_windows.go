//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Thin wrappers over the Win32 calls the window needs.

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	pRegisterClassEx      = user32.NewProc("RegisterClassExW")
	pCreateWindowEx       = user32.NewProc("CreateWindowExW")
	pDefWindowProc        = user32.NewProc("DefWindowProcW")
	pGetMessage           = user32.NewProc("GetMessageW")
	pIsDialogMessage      = user32.NewProc("IsDialogMessageW")
	pTranslateMessage     = user32.NewProc("TranslateMessage")
	pDispatchMessage      = user32.NewProc("DispatchMessageW")
	pPostQuitMessage      = user32.NewProc("PostQuitMessage")
	pPostMessage          = user32.NewProc("PostMessageW")
	pSendMessage          = user32.NewProc("SendMessageW")
	pShowWindow           = user32.NewProc("ShowWindow")
	pSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	pDestroyWindow        = user32.NewProc("DestroyWindow")
	pMoveWindow           = user32.NewProc("MoveWindow")
	pSetWindowPos         = user32.NewProc("SetWindowPos")
	pSetWindowText        = user32.NewProc("SetWindowTextW")
	pInvalidateRect       = user32.NewProc("InvalidateRect")
	pSetTimer             = user32.NewProc("SetTimer")
	pLoadCursor           = user32.NewProc("LoadCursorW")
	pLoadImage            = user32.NewProc("LoadImageW")
	pLoadIcon             = user32.NewProc("LoadIconW")
	pMessageBox           = user32.NewProc("MessageBoxW")
	pFillRect             = user32.NewProc("FillRect")
	pFrameRect            = user32.NewProc("FrameRect")
	pDrawFocusRect        = user32.NewProc("DrawFocusRect")
	pGetSysColor          = user32.NewProc("GetSysColor")
	pGetSysColorBrush     = user32.NewProc("GetSysColorBrush")
	pAdjustWindowRectEx   = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	pGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	pGetDC                = user32.NewProc("GetDC")
	pReleaseDC            = user32.NewProc("ReleaseDC")
	pFindWindow           = user32.NewProc("FindWindowW")
	pRegisterWindowMsg    = user32.NewProc("RegisterWindowMessageW")
	pGetWindowThreadPID   = user32.NewProc("GetWindowThreadProcessId")
	pAllowSetForeground   = user32.NewProc("AllowSetForegroundWindow")
	pEnableWindow         = user32.NewProc("EnableWindow")
	pCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	pAppendMenu           = user32.NewProc("AppendMenuW")
	pTrackPopupMenu       = user32.NewProc("TrackPopupMenu")
	pDestroyMenu          = user32.NewProc("DestroyMenu")
	pGetCursorPos         = user32.NewProc("GetCursorPos")
	pIsWindowVisible      = user32.NewProc("IsWindowVisible")
	pRedrawWindow         = user32.NewProc("RedrawWindow")
	gdi32                 = syscall.NewLazyDLL("gdi32.dll")
	pCreateFont           = gdi32.NewProc("CreateFontW")
	pCreateSolidBrush     = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pSetTextColor         = gdi32.NewProc("SetTextColor")
	pSetBkColor           = gdi32.NewProc("SetBkColor")
	pGetDeviceCaps        = gdi32.NewProc("GetDeviceCaps")
	comctl32              = syscall.NewLazyDLL("comctl32.dll")
	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	comdlg32              = syscall.NewLazyDLL("comdlg32.dll")
	pChooseColor          = comdlg32.NewProc("ChooseColorW")
	shell32               = syscall.NewLazyDLL("shell32.dll")
	pShellExecute         = shell32.NewProc("ShellExecuteW")
	pShellNotifyIcon      = shell32.NewProc("Shell_NotifyIconW")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	pGetModuleHandle      = kernel32.NewProc("GetModuleHandleW")
	pCreateMutex          = kernel32.NewProc("CreateMutexW")
	pCreateEvent          = kernel32.NewProc("CreateEventW")
	pSetEvent             = kernel32.NewProc("SetEvent")
	advapi32              = syscall.NewLazyDLL("advapi32.dll")
	pRegCreateKeyEx       = advapi32.NewProc("RegCreateKeyExW")
	pRegSetValueEx        = advapi32.NewProc("RegSetValueExW")
	pRegDeleteValue       = advapi32.NewProc("RegDeleteValueW")
)

const (
	wsOverlapped  = 0x00000000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsTabStop     = 0x00010000
	wsVScroll     = 0x00200000
	wsClipChild   = 0x02000000

	bsPushButton = 0x0
	bsAutoCheck  = 0x3
	bsOwnerDraw  = 0xB
	ssLeft       = 0x0
	ssOwnerDraw  = 0xD
	ssEtchedHorz = 0x10
	ssNoPrefix   = 0x80
	cbsDropList  = 0x3
	tbsNoTicks   = 0x10

	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmSetFont        = 0x0030
	wmDrawItem       = 0x002B
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmHScroll        = 0x0114
	wmCtlColorStatic = 0x0138
	wmUser           = 0x0400
	wmApp            = 0x8000
	wmNull           = 0x0000
	wmLButtonUp      = 0x0202
	wmLButtonDbl     = 0x0203
	wmRButtonUp      = 0x0205

	cbAddString = 0x0143
	cbSetCurSel = 0x014E
	cbGetCurSel = 0x0147
	cbnSelChg   = 1
	bnClicked   = 0
	bmGetCheck  = 0x00F0
	bmSetCheck  = 0x00F1
	tbmGetPos   = wmUser
	tbmSetPos   = wmUser + 5
	tbmSetRange = wmUser + 6

	swHide = 0
	swShow = 5

	mbOK          = 0x0
	mbYesNo       = 0x4
	mbIconInfo    = 0x40
	mbIconWarn    = 0x30
	mbIconQ       = 0x20
	mbSetFg       = 0x10000
	idYes         = 6
	colorBtnFace  = 15
	colorWindow   = 5
	odsFocus      = 0x10
	trackRetCmd   = 0x100
	trackRightBtn = 0x2
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	X, Y    int32
	Private uint32
}

type rect struct{ Left, Top, Right, Bottom int32 }

type point struct{ X, Y int32 }

type drawItem struct {
	CtlType, CtlID, ItemID, ItemAction, ItemState uint32
	HwndItem                                      uintptr
	HDC                                           uintptr
	Rc                                            rect
	ItemData                                      uintptr
}

type chooseColorT struct {
	Size       uint32
	_          uint32
	Owner      uintptr
	Instance   uintptr
	Result     uint32
	_          uint32
	CustColors *[16]uint32
	Flags      uint32
	_          uint32
	CustData   uintptr
	Hook       uintptr
	Template   uintptr
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         guid
	HBalloonIcon     uintptr
}

func u16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func ptr(s string) uintptr { return uintptr(unsafe.Pointer(u16(s))) }

func send(h uintptr, msg uint32, w, l uintptr) uintptr {
	r, _, _ := pSendMessage.Call(h, uintptr(msg), w, l)
	return r
}

func setText(h uintptr, s string) { pSetWindowText.Call(h, ptr(s)) }

func show(h uintptr, visible bool) {
	if visible {
		pShowWindow.Call(h, swShow)
	} else {
		pShowWindow.Call(h, swHide)
	}
}

func messageBox(owner uintptr, text, title string, flags uintptr) int {
	r, _, _ := pMessageBox.Call(owner, ptr(text), ptr(title), flags|mbSetFg)
	return int(r)
}

func colorRef(c RGB) uintptr { return uintptr(c[0]) | uintptr(c[1])<<8 | uintptr(c[2])<<16 }

func fillRect(hdc uintptr, r rect, c RGB) {
	b, _, _ := pCreateSolidBrush.Call(colorRef(c))
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), b)
	pDeleteObject.Call(b)
}

func frameRect(hdc uintptr, r rect, c RGB) {
	b, _, _ := pCreateSolidBrush.Call(colorRef(c))
	pFrameRect.Call(hdc, uintptr(unsafe.Pointer(&r)), b)
	pDeleteObject.Call(b)
}

func openURL(u string) { pShellExecute.Call(0, ptr("open"), ptr(u), 0, 0, 1) }

func systemDPI() int {
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d > 0 {
			return int(d)
		}
	}
	dc, _, _ := pGetDC.Call(0)
	d, _, _ := pGetDeviceCaps.Call(dc, 90 /*LOGPIXELSY*/)
	pReleaseDC.Call(0, dc)
	if d == 0 {
		return 96
	}
	return int(d)
}

// --- registry: HKCU\...\Run ---------------------------------------------------

const hkcu = 0x80000001
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func runKeyOpen() (syscall.Handle, bool) {
	var k syscall.Handle
	r, _, _ := pRegCreateKeyEx.Call(hkcu, ptr(runKey), 0, 0, 0, 0xF003F /*KEY_ALL_ACCESS*/, 0, uintptr(unsafe.Pointer(&k)), 0)
	return k, r == 0
}

func setRunValue(name, value string) error {
	k, ok := runKeyOpen()
	if !ok {
		return syscall.EINVAL
	}
	defer syscall.RegCloseKey(k)
	v, _ := syscall.UTF16FromString(value)
	r, _, _ := pRegSetValueEx.Call(uintptr(k), ptr(name), 0, 1 /*REG_SZ*/, uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func deleteRunValue(name string) {
	if k, ok := runKeyOpen(); ok {
		pRegDeleteValue.Call(uintptr(k), ptr(name))
		syscall.RegCloseKey(k)
	}
}

func getRunValue(name string) string {
	var k syscall.Handle
	if syscall.RegOpenKeyEx(hkcu, u16(runKey), 0, syscall.KEY_READ, &k) != nil {
		return ""
	}
	defer syscall.RegCloseKey(k)
	var buf [1024]uint16
	n := uint32(len(buf) * 2)
	if syscall.RegQueryValueEx(k, u16(name), nil, nil, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}
