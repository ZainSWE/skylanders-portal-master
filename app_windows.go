//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	appTitle      = "Skylanders Portal Master"
	mutexName     = "Local\\PortalMaster.Running"
	trayClass     = "PortalMasterTray"
	oldTrayClass  = "PortalLightsTray"   // Portal Lights 3-7, this app's earlier name
	oldGUIClass   = "PortalLightsWindow" // the previous native-window version
	runName       = "SkylandersPortalMaster"
	oldRunName    = "PortalLights"
	wmTray        = wmApp + 1
	wmQuitApp     = wmApp + 2
	idTrayOpen    = 1
	idTrayQuit    = 2
	edgeWindowCls = "Chrome_WidgetWin_1"
)

func init() { runtime.LockOSThread() } // the tray window lives on the main thread

// ---------- paths ----------

func appDir() string {
	d := filepath.Join(os.Getenv("APPDATA"), "PortalMaster")
	os.MkdirAll(d, 0o755)
	return d
}
func oldAppDir() string     { return filepath.Join(os.Getenv("APPDATA"), "PortalLights") }
func oldInstallDir() string { return filepath.Join(os.Getenv("LOCALAPPDATA"), "PortalLights") }
func logPath() string       { return filepath.Join(appDir(), "log.txt") }
func settingsPath() string  { return filepath.Join(appDir(), "settings.txt") }
func installDir() string    { return filepath.Join(os.Getenv("LOCALAPPDATA"), "PortalMaster") }
func installedExe() string  { return filepath.Join(installDir(), "SkylandersPortalMaster.exe") }
func shortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Skylanders Portal Master.lnk`)
}
func oldShortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Portal Lights.lnk`)
}

// ---------- moving over from Portal Lights ----------

// stopPortalLights closes a running copy of this app from when it was called Portal Lights.
func stopPortalLights() bool {
	h, _, _ := pFindWindow.Call(ptr(oldTrayClass), 0)
	if h == 0 {
		return false
	}
	var pid uint32
	pGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	proc, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE|syscall.SYNCHRONIZE, false, pid)
	pPostMessage.Call(h, wmQuitApp, 0, 0) // let it switch the portal off tidily
	if err == nil {
		if r, _ := syscall.WaitForSingleObject(proc, 4000); r != syscall.WAIT_OBJECT_0 {
			syscall.TerminateProcess(proc, 0)
			syscall.WaitForSingleObject(proc, 2000)
		}
		syscall.CloseHandle(proc)
	}
	return true
}

// migrateFromPortalLights carries settings and portal profiles over from the old
// name, then removes the old startup entry, shortcut and program. It returns whether the old
// version was set to start with Windows.
func migrateFromPortalLights() (hadStartup bool) {
	hadStartup = getRunValue(oldRunName) != ""
	old := oldAppDir()
	if _, err := os.Stat(old); err != nil {
		return hadStartup
	}
	newDir := filepath.Join(os.Getenv("APPDATA"), "PortalMaster")
	if _, err := os.Stat(filepath.Join(newDir, "settings.txt")); err == nil {
		return hadStartup // already moved
	}
	os.MkdirAll(newDir, 0o755)
	for _, name := range []string{"settings.txt", "portals.json"} {
		copyFile(filepath.Join(old, name), filepath.Join(newDir, name))
	}
	return hadStartup
}

func removePortalLights() {
	deleteRunValue(oldRunName)
	os.Remove(oldShortcutPath())
	os.Remove(filepath.Join(oldInstallDir(), "PortalLights.exe"))
	os.RemoveAll(oldInstallDir()) // best effort: its browser window may still be open
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// install copies the program somewhere permanent and adds a Start menu shortcut.
func install() {
	self, _ := os.Executable()
	if !samePath(self, installedExe()) {
		if err := copyFile(self, installedExe()); err != nil {
			log.Println("install copy:", err)
		}
	}
	if _, err := os.Stat(installedExe()); err == nil {
		if err := createShortcut(shortcutPath(), installedExe(), "", "Control your Skylanders portal's lights and speaker"); err != nil {
			log.Println("shortcut:", err)
		}
	}
}

func startupEnabled() bool { return getRunValue(runName) != "" }

func setStartup(on bool) error {
	if !on {
		deleteRunValue(runName)
		return nil
	}
	exe := installedExe()
	if _, err := os.Stat(exe); err != nil {
		install()
	}
	if _, err := os.Stat(exe); err != nil {
		exe, _ = os.Executable()
	}
	return setRunValue(runName, `"`+exe+`" --background`)
}

// ---------- older versions ----------

// stopOldVersions turns off and removes the earlier one-file programs and the
// previous Portal Lights window so they don't fight this one for the portal.
func stopOldVersions() (found bool) {
	startup := filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`)
	for _, name := range []string{"SkylandersPortalLights", "TrapTeamPortalLights"} {
		h, _, _ := pCreateEvent.Call(0, 1, 0, ptr("Local\\"+name+".Stop"))
		pSetEvent.Call(h)
		exe := filepath.Join(startup, name+".exe")
		if _, err := os.Stat(exe); err == nil {
			found = true
			for i := 0; i < 20; i++ {
				if err := os.Remove(exe); err == nil || errors.Is(err, os.ErrNotExist) {
					log.Println("removed old version:", exe)
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
		}
	}
	if h, _, _ := pFindWindow.Call(ptr(oldGUIClass), 0); h != 0 {
		var pid uint32
		pGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if proc, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE|syscall.SYNCHRONIZE, false, pid); err == nil {
			syscall.TerminateProcess(proc, 0)
			syscall.WaitForSingleObject(proc, 3000)
			syscall.CloseHandle(proc)
			log.Println("closed the previous Portal Lights window version")
		}
	}
	return found
}

func firstSettings() (Settings, bool) {
	if _, err := os.Stat(settingsPath()); err == nil {
		return loadSettings(settingsPath()), false
	}
	s := defaults()
	old := filepath.Join(os.Getenv("APPDATA"), "SkylandersPortalLights", "settings.txt")
	if b, err := os.ReadFile(old); err == nil {
		s = parseSettings(string(b))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("APPDATA"), "TrapTeamPortalLights", "settings.txt")); err == nil {
		s.Type = "trapteam"
	}
	return s, true
}

// ---------- the app (implements Backend) ----------

type App struct {
	engine    *Engine
	player    *Player
	server    *Server
	hwnd      uintptr
	icon      uintptr
	showMsg   uint32
	taskbar   uint32
	saveMu    sync.Mutex
	saveTimer *time.Timer
	lastState int
	quitOnce  sync.Once
}

func (a *App) State() APIState {
	st := a.engine.Status()
	var devs []APIDevice
	for _, d := range st.Devices {
		devs = append(devs, APIDevice{Label: d.Label(), Ready: d.IsWinUSB() || d.IsHID()})
	}
	portals := a.engine.Portals()
	var info []string
	for _, p := range portals {
		info = append(info, p.Name+" ("+p.USBID+")\n"+p.Info)
	}
	return APIState{
		Settings:   stateSettings(a.engine.Settings()),
		Status:     APIStatus{State: stateNames[st.State], Text: st.Text, Device: st.Device, DriverNeeded: st.DriverNeeded},
		Devices:    devs,
		DeviceInfo: strings.Join(info, "\n\n"),
		Portals:    portals,
		Arrival:    a.engine.Arrival(),
		Routes:     a.engine.Routes(),
		Frame:      apiFrame(a.engine.Frame()),
		Audio:      a.player.State(),
		Startup:    startupEnabled(),
		LogPath:    logPath(),
	}
}

func (a *App) SetPortalType(key, typ string) { a.engine.SetPortalType(key, typ) }

func profilesPath() string { return filepath.Join(appDir(), "portals.json") }

func loadProfiles(fallback Settings) map[string]Profile {
	b, err := os.ReadFile(profilesPath())
	if err != nil {
		return map[string]Profile{}
	}
	return decodeProfiles(b, fallback)
}

func saveProfiles(m map[string]Profile) {
	if err := os.WriteFile(profilesPath(), encodeProfiles(m), 0o644); err != nil {
		log.Println("couldn't save portal settings:", err)
	}
}

func (a *App) UpdatePortal(key string, patch map[string]json.RawMessage) {
	a.engine.SetPortalLights(key, patch)
	a.scheduleSave()
}

func (a *App) Update(patch map[string]json.RawMessage) {
	a.engine.SetSettings(ApplyPatch(a.engine.Settings(), patch))
	a.scheduleSave()
}

func (a *App) scheduleSave() {
	a.saveMu.Lock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	a.saveTimer = time.AfterFunc(400*time.Millisecond, a.save)
	a.saveMu.Unlock()
}

func (a *App) save() {
	if err := saveSettings(settingsPath(), a.engine.Settings()); err != nil {
		log.Println("couldn't save settings:", err)
	}
}

func (a *App) SetStartup(on bool) error { return setStartup(on) }

func (a *App) PlayPCM(name string, pcm []byte) error {
	return a.player.PlayFrames("file", nonEmpty(name, "Sound"), PCMToFrames(pcm))
}

func (a *App) PlayTest(variant int) error {
	return a.player.PlayFrames("test", "Test chime", Chime())
}

func (a *App) StopAudio()           { a.player.Stop() }
func (a *App) PlayFX(ev SoundEvent) { go a.player.PlayEvent(ev) }

func (a *App) Open(what string) {
	switch what {
	case "zadig":
		openURL("https://zadig.akeo.ie/")
	case "log":
		pShellExecute.Call(0, ptr("open"), ptr(logPath()), 0, 0, 1)
	}
}

func (a *App) DataDir() string { return appDir() }

func (a *App) Quit() { pPostMessage.Call(a.hwnd, wmQuitApp, 0, 0) }

// powerOff plays the turning-off sound for each connected portal and waits for it to finish.
func (a *App) powerOff() {
	if !a.engine.Settings().PowerSounds {
		return
	}
	done := make(chan struct{})
	go func() {
		played := map[string]bool{}
		for _, typ := range a.engine.ConnectedTypes() {
			if !played[typ] {
				played[typ] = true
				a.player.PlayInTurn(SoundEvent{Style: typ, Kind: "off"})
			}
		}
		time.Sleep(300 * time.Millisecond) // let the last bit reach the portal
		close(done)
	}()
	select { // never hold up quitting for long
	case <-done:
	case <-time.After(4 * time.Second):
	}
}

func (a *App) shutdown() {
	a.quitOnce.Do(func() {
		log.Println("shutting down")
		a.powerOff()
		a.player.Shutdown()
		log.Println("shutdown: sound off")
		a.engine.Stop()
		log.Println("shutdown: lights off")
		a.save()
		a.removeTray()
		pDestroyWindow.Call(a.hwnd)
		log.Println("shutdown: window closed")
	})
}

// ---------- window (opens in Edge as an app window) ----------

func findEdge() string {
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		p := filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (a *App) openUI() {
	if h, _, _ := pFindWindow.Call(ptr(edgeWindowCls), ptr(appTitle)); h != 0 {
		pShowWindow.Call(h, 9) // SW_RESTORE
		pSetForegroundWindow.Call(h)
		return
	}
	url := a.server.URL()
	if edge := findEdge(); edge != "" {
		cmd := exec.Command(edge, "--app="+url, "--user-data-dir="+filepath.Join(installDir(), "window"),
			"--window-size=1140,800", "--no-first-run", "--no-default-browser-check", "--disable-features=Translate")
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return
		}
	}
	openURL(url) // no Edge: use the default browser
}

// ---------- tray ----------

func (a *App) trayData() notifyIconData {
	n := notifyIconData{HWnd: a.hwnd, UID: 1, UFlags: 0x1 | 0x2 | 0x4, UCallbackMessage: wmTray, HIcon: a.icon}
	n.CbSize = uint32(unsafe.Sizeof(n))
	copy(n.SzTip[:], syscall.StringToUTF16(appTitle))
	return n
}

func (a *App) addTray() {
	n := a.trayData()
	pShellNotifyIcon.Call(0 /*NIM_ADD*/, uintptr(unsafe.Pointer(&n)))
}

func (a *App) removeTray() {
	n := a.trayData()
	pShellNotifyIcon.Call(2 /*NIM_DELETE*/, uintptr(unsafe.Pointer(&n)))
}

func (a *App) balloon(title, text string) {
	n := a.trayData()
	n.UFlags |= 0x10 // NIF_INFO
	copy(n.SzInfo[:255], syscall.StringToUTF16(text))
	copy(n.SzInfoTitle[:63], syscall.StringToUTF16(title))
	n.DwInfoFlags = 1
	pShellNotifyIcon.Call(1 /*NIM_MODIFY*/, uintptr(unsafe.Pointer(&n)))
}

func (a *App) trayMenu() {
	m, _, _ := pCreatePopupMenu.Call()
	pAppendMenu.Call(m, 0, idTrayOpen, ptr("Open Skylanders Portal Master"))
	pAppendMenu.Call(m, 0x800, 0, 0)
	pAppendMenu.Call(m, 0, idTrayQuit, ptr("Turn lights off && quit"))
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(a.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(m, trackRetCmd|trackRightBtn, uintptr(pt.X), uintptr(pt.Y), 0, a.hwnd, 0)
	pPostMessage.Call(a.hwnd, wmNull, 0, 0)
	pDestroyMenu.Call(m)
	switch cmd {
	case idTrayOpen:
		a.openUI()
	case idTrayQuit:
		a.shutdown()
	}
}

// watch shows a tray notification when something needs attention while the window is closed.
func (a *App) watch() {
	st := a.engine.Status().State
	if st == a.lastState {
		return
	}
	prev := a.lastState
	a.lastState = st
	if h, _, _ := pFindWindow.Call(ptr(edgeWindowCls), ptr(appTitle)); h != 0 || prev == -1 {
		return
	}
	switch st {
	case stNeedsDriver:
		a.balloon("Your portal needs a quick fix", "Click here to open Skylanders Portal Master and fix it.")
	case stConnected:
		a.balloon("Portal connected", "The lights are on.")
	}
}

var app *App

func trayProc(hwnd, msg, wp, lp uintptr) uintptr {
	a := app
	if a != nil {
		switch uint32(msg) {
		case a.showMsg:
			a.openUI()
			return 0
		case a.taskbar:
			a.addTray()
			return 0
		case wmTray:
			switch uint32(lp) {
			case wmLButtonUp, wmLButtonDbl, 0x405 /*NIN_BALLOONUSERCLICK*/ :
				a.openUI()
			case wmRButtonUp:
				a.trayMenu()
			}
			return 0
		case wmQuitApp:
			a.shutdown()
			return 0
		case wmTimer:
			a.watch()
			return 0
		case wmDestroy:
			pPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

func registerShowMessage() uint32 {
	r, _, _ := pRegisterWindowMsg.Call(ptr("PortalMaster.Open"))
	return uint32(r)
}

func hasArg(a string) bool {
	for _, x := range os.Args[1:] {
		if strings.EqualFold(x, a) {
			return true
		}
	}
	return false
}

func main() {
	background := hasArg("--background")

	_, _, mutexErr := pCreateMutex.Call(0, 0, ptr(mutexName))
	if mutexErr == syscall.ERROR_ALREADY_EXISTS {
		if !background { // already running: ask it to open its window
			if h, _, _ := pFindWindow.Call(ptr(trayClass), 0); h != 0 {
				pAllowSetForeground.Call(uintptr(^uint32(0))) // ASFW_ANY
				pPostMessage.Call(h, uintptr(registerShowMessage()), 0, 0)
			}
		}
		return
	}

	hadOld := stopPortalLights()
	go func() { // an old copy that was still starting up (e.g. both launched at sign-in)
		for i := 0; i < 20; i++ {
			time.Sleep(3 * time.Second)
			if stopPortalLights() {
				log.Println("closed a copy of Portal Lights that started late")
			}
		}
	}()
	oldStartup := migrateFromPortalLights()
	if lf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}
	log.Println("Skylanders Portal Master", version, "starting, background =", background, "replaced Portal Lights =", hadOld)

	pCoInitializeEx.Call(0, 2) // apartment-threaded, for the Start menu shortcut
	cfg, first := firstSettings()
	stopOldVersions()
	install()
	removePortalLights()
	if oldStartup && !startupEnabled() {
		setStartup(true)
	}
	if first {
		saveSettings(settingsPath(), cfg)
		if err := setStartup(true); err != nil {
			log.Println("couldn't enable startup:", err)
		}
	} else if startupEnabled() {
		setStartup(true) // point startup at the installed copy
	}

	engine := NewEngine(cfg, loadProfiles(cfg), saveProfiles)
	player := NewPlayer(engine)
	engine.OnFigure = func(f FigureInfo, typ string, alreadyOn bool) {
		if alreadyOn && engine.Settings().PowerSounds {
			return // figures already on a portal as it connects: the power-up sound covers them
		}
		player.PlayEvent(SoundEvent{Style: typ, Kind: "place"}) // plays once even if several land together
	}
	engine.OnConnect = func(typ string) {
		player.PlayInTurn(SoundEvent{Style: typ, Kind: "on"})
	}
	go engine.Run()
	go player.Run()
	go player.watchOutputs()
	a := &App{engine: engine, player: player, lastState: -1, showMsg: registerShowMessage()}
	app = a
	srv, err := StartServer(a)
	if err != nil {
		messageBox(0, "Skylanders Portal Master couldn't start its window: "+err.Error(), appTitle, mbOK|mbIconWarn)
		return
	}
	a.server = srv
	if os.Getenv("PORTALMASTER_DEBUG_URL") != "" {
		os.WriteFile(os.Getenv("PORTALMASTER_DEBUG_URL"), []byte(srv.URL()), 0o600)
	}

	inst, _, _ := pGetModuleHandle.Call(0)
	sx, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	a.icon, _, _ = pLoadImage.Call(inst, 1, 1, sx, sx, 0)
	if a.icon == 0 {
		a.icon, _, _ = pLoadIcon.Call(0, 32512)
	}
	r, _, _ := pRegisterWindowMsg.Call(ptr("TaskbarCreated"))
	a.taskbar = uint32(r)
	wc := wndClassEx{WndProc: syscall.NewCallback(trayProc), Instance: inst, Icon: a.icon, IconSm: a.icon, ClassName: u16(trayClass)}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	a.hwnd, _, _ = pCreateWindowEx.Call(0, ptr(trayClass), ptr(appTitle), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	a.addTray()
	pSetTimer.Call(a.hwnd, 1, 1000, 0)

	if !background {
		a.openUI()
	}

	var m msgT
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	a.shutdown()
	log.Println("Skylanders Portal Master stopped")
}

// ---------- Start menu shortcut (IShellLink) ----------

var (
	clsidShellLink  = guid{0x00021401, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = guid{0x000214F9, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = guid{0x0000010B, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

func createShortcut(lnk, target, args, desc string) error {
	var link, file uintptr
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, 1, /*INPROC*/
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link))); failed(hr) {
		return hresultError{"creating shortcut", hr}
	}
	defer comRelease(link)
	comCall(link, 20, ptr(target))              // SetPath
	comCall(link, 11, ptr(args))                // SetArguments
	comCall(link, 7, ptr(desc))                 // SetDescription
	comCall(link, 9, ptr(filepath.Dir(target))) // SetWorkingDirectory
	comCall(link, 17, ptr(target), 0)           // SetIconLocation
	if hr := comCall(link, 0, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&file))); failed(hr) {
		return hresultError{"saving shortcut", hr}
	}
	defer comRelease(file)
	if hr := comCall(file, 6, ptr(lnk), 1); failed(hr) { // Save
		return hresultError{"saving shortcut", hr}
	}
	return nil
}
