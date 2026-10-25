//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

const (
	digcfPres  = 0x02
	digcfAll   = 0x04
	digcfIface = 0x10
	invalid    = ^uintptr(0)

	spdrpDeviceDesc   = 0x00
	spdrpHardwareID   = 0x01
	spdrpService      = 0x04
	spdrpFriendlyName = 0x0C

	pipeTimeout = 0x03 // PIPE_TRANSFER_TIMEOUT
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	guidUSBDevice = guid{0xA5DCBF10, 0x6530, 0x11D2, [8]byte{0x90, 0x1F, 0x00, 0xC0, 0x4F, 0xB9, 0x51, 0xED}}
	guidHID       = guid{0x4D1E55B2, 0xF16F, 0x11CF, [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30}}
)

type ifaceData struct {
	CbSize   uint32
	Guid     guid
	Flags    uint32
	Reserved uintptr
}

type devInfoData struct {
	CbSize    uint32
	ClassGuid guid
	DevInst   uint32
	Reserved  uintptr
}

type pipeInfo struct {
	PipeType  uint32
	PipeID    uint8
	_         uint8
	MaxPacket uint16
	Interval  uint8
	_         [3]byte
}

var (
	setupapi           = syscall.NewLazyDLL("setupapi.dll")
	procGetClassDevs   = setupapi.NewProc("SetupDiGetClassDevsW")
	procEnumIfaces     = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procIfaceDetail    = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procDestroyList    = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	procEnumDevInfo    = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procOpenDevRegKey  = setupapi.NewProc("SetupDiOpenDevRegKey")
	procGetDevRegProp  = setupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")
	procGetInstanceID  = setupapi.NewProc("SetupDiGetDeviceInstanceIdW")
	ole32              = syscall.NewLazyDLL("ole32.dll")
	procCLSIDFromStr   = ole32.NewProc("CLSIDFromString")
	winusb             = syscall.NewLazyDLL("winusb.dll")
	procWUInit         = winusb.NewProc("WinUsb_Initialize")
	procWUFree         = winusb.NewProc("WinUsb_Free")
	procWUQueryPipe    = winusb.NewProc("WinUsb_QueryPipe")
	procWUSetPolicy    = winusb.NewProc("WinUsb_SetPipePolicy")
	procWUWrite        = winusb.NewProc("WinUsb_WritePipe")
	procWURead         = winusb.NewProc("WinUsb_ReadPipe")
	procWUGetDesc      = winusb.NewProc("WinUsb_GetDescriptor")
	hid                = syscall.NewLazyDLL("hid.dll")
	procHidSetOutput   = hid.NewProc("HidD_SetOutputReport")
	procHidGetPreparse = hid.NewProc("HidD_GetPreparsedData")
	procHidFreePrepare = hid.NewProc("HidD_FreePreparsedData")
	procHidGetCaps     = hid.NewProc("HidP_GetCaps")
)

// Detected is a USB device that looks like a Skylanders portal.
type Detected struct {
	VID, PID string // lower-case hex, e.g. "1430", "1f17"
	Name     string
	Service  string // driver service, e.g. "WinUSB", "HidUsb", "xusb22"
	guids    []guid // WinUSB device interface GUIDs from the registry
	key      string // "vid_1430&pid_1f17"
	Instance string // Windows device instance ID, unique per plugged-in portal
}

// pathKey is how this device's instance ID appears inside its interface paths.
func (d Detected) pathKey() string {
	if d.Instance == "" {
		return d.key
	}
	return strings.ToLower(strings.ReplaceAll(d.Instance, `\`, "#"))
}

func (d Detected) Label() string {
	name := d.Name
	if name == "" {
		name = "Unknown device"
	}
	svc := d.Service
	if svc == "" {
		svc = "no driver"
	}
	return fmt.Sprintf("%s  (USB ID %s:%s, driver: %s)", name, strings.ToUpper(d.VID), strings.ToUpper(d.PID), svc)
}

func (d Detected) IsWinUSB() bool { return strings.EqualFold(d.Service, "WinUSB") }
func (d Detected) IsHID() bool    { return strings.EqualFold(d.Service, "HidUsb") }

// IsCompositeParent is the umbrella node Windows makes for multi-interface devices.
func (d Detected) IsCompositeParent() bool { return strings.EqualFold(d.Service, "usbccgp") }

func regString(h uintptr, d *devInfoData, prop uintptr) string {
	var buf [1024]uint16
	var typ, need uint32
	ok, _, _ := procGetDevRegProp.Call(h, uintptr(unsafe.Pointer(d)), prop, uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&need)))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:]) // first string of a multi-sz
}

func parseVIDPID(hw string) (vid, pid string, ok bool) {
	hw = strings.ToLower(hw)
	i := strings.Index(hw, "vid_")
	j := strings.Index(hw, "pid_")
	if i < 0 || j < 0 || i+8 > len(hw) || j+8 > len(hw) {
		return "", "", false
	}
	return hw[i+4 : i+8], hw[j+4 : j+8], true
}

func looksLikePortal(vid, name string) bool {
	n := strings.ToLower(name)
	return vid == "1430" || strings.Contains(n, "skylander") || strings.Contains(n, "portal of power") ||
		strings.Contains(n, "traptanium")
}

// scanDevices lists every connected USB device that looks like a portal.
func scanDevices() []Detected {
	usb, _ := syscall.UTF16PtrFromString("USB")
	h, _, _ := procGetClassDevs.Call(0, uintptr(unsafe.Pointer(usb)), 0, digcfPres|digcfAll)
	if h == invalid {
		return nil
	}
	defer procDestroyList.Call(h)
	var out []Detected
	for i := uintptr(0); i < 2048; i++ {
		d := devInfoData{CbSize: uint32(unsafe.Sizeof(devInfoData{}))}
		ok, _, _ := procEnumDevInfo.Call(h, i, uintptr(unsafe.Pointer(&d)))
		if ok == 0 {
			break
		}
		hwid := strings.ToLower(regString(h, &d, spdrpHardwareID))
		vid, pid, ok2 := parseVIDPID(hwid)
		if !ok2 {
			continue
		}
		name := regString(h, &d, spdrpFriendlyName)
		if name == "" {
			name = regString(h, &d, spdrpDeviceDesc)
		}
		if k := strings.LastIndex(name, ";"); k >= 0 { // "@oem12.inf,%desc%;Real Name"
			name = name[k+1:]
		}
		if !looksLikePortal(vid, name) {
			continue
		}
		det := Detected{VID: vid, PID: pid, Name: name, Service: regString(h, &d, spdrpService),
			key: "vid_" + vid + "&pid_" + pid}
		if k := strings.Index(hwid, "&mi_"); k >= 0 && k+6 <= len(hwid) { // one interface of a composite device
			det.key += hwid[k : k+6]
		}
		var idBuf [512]uint16
		if ok, _, _ := procGetInstanceID.Call(h, uintptr(unsafe.Pointer(&d)), uintptr(unsafe.Pointer(&idBuf[0])), uintptr(len(idBuf)), 0); ok != 0 {
			det.Instance = syscall.UTF16ToString(idBuf[:])
		}
		if det.IsWinUSB() {
			det.guids = interfaceGUIDs(h, &d)
		}
		out = append(out, det)
	}
	return out
}

func interfaceGUIDs(h uintptr, d *devInfoData) []guid {
	key, _, _ := procOpenDevRegKey.Call(h, uintptr(unsafe.Pointer(d)), 1 /*DICS_FLAG_GLOBAL*/, 0, 1 /*DIREG_DEV*/, syscall.KEY_READ)
	if key == invalid {
		return nil
	}
	defer syscall.RegCloseKey(syscall.Handle(key))
	var out []guid
	for _, name := range []string{"DeviceInterfaceGUIDs", "DeviceInterfaceGUID"} {
		var val [512]uint16
		n := uint32(len(val) * 2)
		np, _ := syscall.UTF16PtrFromString(name)
		if syscall.RegQueryValueEx(syscall.Handle(key), np, nil, nil, (*byte)(unsafe.Pointer(&val[0])), &n) == nil {
			sp, _ := syscall.UTF16PtrFromString(syscall.UTF16ToString(val[:]))
			var g guid
			if hr, _, _ := procCLSIDFromStr.Call(uintptr(unsafe.Pointer(sp)), uintptr(unsafe.Pointer(&g))); hr == 0 {
				out = append(out, g)
			}
		}
	}
	return out
}

// interfacePaths lists device paths for every present interface of class g.
func interfacePaths(g *guid) []string {
	h, _, _ := procGetClassDevs.Call(uintptr(unsafe.Pointer(g)), 0, 0, digcfPres|digcfIface)
	if h == invalid {
		return nil
	}
	defer procDestroyList.Call(h)
	var out []string
	for i := uintptr(0); i < 256; i++ {
		d := ifaceData{CbSize: uint32(unsafe.Sizeof(ifaceData{}))}
		ok, _, _ := procEnumIfaces.Call(h, 0, uintptr(unsafe.Pointer(g)), i, uintptr(unsafe.Pointer(&d)))
		if ok == 0 {
			break
		}
		var need uint32
		procIfaceDetail.Call(h, uintptr(unsafe.Pointer(&d)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
		if need < 8 {
			continue
		}
		buf := make([]uint64, need/8+2)
		*(*uint32)(unsafe.Pointer(&buf[0])) = 8 // sizeof(SP_DEVICE_INTERFACE_DETAIL_DATA_W) on x64
		ok, _, _ = procIfaceDetail.Call(h, uintptr(unsafe.Pointer(&d)), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0, 0)
		if ok == 0 {
			continue
		}
		p := unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(&buf[0]), 4)), (need-4)/2)
		out = append(out, syscall.UTF16ToString(p))
	}
	return out
}

// ---------------------------------------------------------------------------
