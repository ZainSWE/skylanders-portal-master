//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Captures whatever is playing on the PC's default speakers (WASAPI loopback)
// and hands it over as 8 kHz stereo frames.

var (
	pCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	pCoUninitialize   = ole32.NewProc("CoUninitialize")
	pCoCreateInstance = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	pPropVariantClear = ole32.NewProc("PropVariantClear")
)

// errOutputChanged means the output being copied should change (e.g. headphones were plugged
// in and became the default), so capture restarts straight away on the new one.
var errOutputChanged = errors.New("sound output changed")

type propertyKey struct {
	fmtid guid
	pid   uint32
}

var pkeyFriendlyName = propertyKey{guid{0xA45C254E, 0xDF1C, 0x4EFD, [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}}, 14}

// wideString reads a NUL-terminated UTF-16 string.
func wideString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var u []uint16
	for i := uintptr(0); i < 4096; i++ {
		c := *(*uint16)(unsafe.Pointer(p + i*2))
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return syscall.UTF16ToString(u)
}

func deviceID(dev uintptr) string {
	var p uintptr
	if hr := comCall(dev, 5, uintptr(unsafe.Pointer(&p))); failed(hr) {
		return ""
	}
	defer pCoTaskMemFree.Call(p)
	return wideString(p)
}

func deviceName(dev uintptr) string {
	var store uintptr
	if hr := comCall(dev, 4, 0 /*STGM_READ*/, uintptr(unsafe.Pointer(&store))); failed(hr) {
		return ""
	}
	defer comRelease(store)
	var pv [24]byte // PROPVARIANT
	if hr := comCall(store, 5, uintptr(unsafe.Pointer(&pkeyFriendlyName)), uintptr(unsafe.Pointer(&pv[0]))); failed(hr) {
		return ""
	}
	defer pPropVariantClear.Call(uintptr(unsafe.Pointer(&pv[0])))
	if *(*uint16)(unsafe.Pointer(&pv[0])) != 31 { // VT_LPWSTR
		return ""
	}
	return wideString(*(*uintptr)(unsafe.Pointer(&pv[8])))
}

func defaultOutputID(enum uintptr) string {
	var dev uintptr
	if hr := comCall(enum, 4, 0 /*eRender*/, 0 /*eConsole*/, uintptr(unsafe.Pointer(&dev))); failed(hr) {
		return ""
	}
	defer comRelease(dev)
	return deviceID(dev)
}

// listOutputs lists the PC's active sound outputs (speakers, headphones, ...).
func listOutputs() ([]APIOutput, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0)
	defer pCoUninitialize.Call()
	var enum, coll uintptr
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, 0x17,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); failed(hr) {
		return nil, hresultError{"opening the sound system", hr}
	}
	defer comRelease(enum)
	def := defaultOutputID(enum)
	if hr := comCall(enum, 3, 0 /*eRender*/, 1 /*DEVICE_STATE_ACTIVE*/, uintptr(unsafe.Pointer(&coll))); failed(hr) {
		return nil, hresultError{"listing sound outputs", hr}
	}
	defer comRelease(coll)
	var n uint32
	comCall(coll, 3, uintptr(unsafe.Pointer(&n)))
	var out []APIOutput
	for i := uint32(0); i < n && i < 64; i++ {
		var dev uintptr
		if hr := comCall(coll, 4, uintptr(i), uintptr(unsafe.Pointer(&dev))); failed(hr) {
			continue
		}
		id := deviceID(dev)
		name := deviceName(dev)
		comRelease(dev)
		if id == "" {
			continue
		}
		if name == "" {
			name = "Sound output " + fmt.Sprint(i+1)
		}
		out = append(out, APIOutput{ID: id, Name: name, Default: id == def})
	}
	return out, nil
}

var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = guid{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

// comCall calls method number idx on a COM object.
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

type hresultError struct {
	what string
	hr   uintptr
}

func (e hresultError) Error() string { return fmt.Sprintf("%s failed (0x%08X)", e.what, uint32(e.hr)) }

func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

// captureLoopback copies the sound playing on an output (want = its ID, or "" for whatever is
// the default) until stop is closed, the device goes away, or the output to use changes.
// hearing is told the name of the output being copied.
func captureLoopback(stop <-chan struct{}, want string, hearing func(string), out func([][2]float32)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
	defer pCoUninitialize.Call()

	var enum, dev, client, capture uintptr
	defer func() { comRelease(capture); comRelease(client); comRelease(dev); comRelease(enum) }()

	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, 0x17,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); failed(hr) {
		return hresultError{"opening the sound system", hr}
	}
	flow, flags := uintptr(0), uintptr(0x00020000) // speakers, loopback
	if os.Getenv("PORTALMASTER_TEST_CAPTURE") != "" {
		flow, flags = 1, 0 // test hook: record the default input instead
	}
	following := true // following the default output (or waiting for the chosen one to come back)
	if want != "" && flow == 0 {
		w, _ := syscall.UTF16PtrFromString(want)
		if hr := comCall(enum, 5, uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(&dev))); !failed(hr) {
			var st uint32
			comCall(dev, 6, uintptr(unsafe.Pointer(&st)))
			if st == 1 { // active
				following = false
			} else {
				comRelease(dev)
				dev = 0
			}
		} else {
			dev = 0
		}
	}
	if dev == 0 {
		if hr := comCall(enum, 4, flow /*eRender*/, 0 /*eConsole*/, uintptr(unsafe.Pointer(&dev))); failed(hr) {
			return hresultError{"finding your speakers", hr}
		}
	}
	startDefault := defaultOutputID(enum)
	hearing(deviceName(dev))
	lastCheck := time.Now()
	if hr := comCall(dev, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), 0x17, 0, uintptr(unsafe.Pointer(&client))); failed(hr) {
		return hresultError{"opening your speakers", hr}
	}
	var wfx uintptr
	if hr := comCall(client, 8, uintptr(unsafe.Pointer(&wfx))); failed(hr) {
		return hresultError{"reading the sound format", hr}
	}
	defer pCoTaskMemFree.Call(wfx)

	tag := *(*uint16)(unsafe.Pointer(wfx))
	channels := int(*(*uint16)(unsafe.Pointer(wfx + 2)))
	rate := *(*uint32)(unsafe.Pointer(wfx + 4))
	block := int(*(*uint16)(unsafe.Pointer(wfx + 12)))
	bits := int(*(*uint16)(unsafe.Pointer(wfx + 14)))
	isFloat := tag == 3
	if tag == 0xFFFE {
		isFloat = *(*uint32)(unsafe.Pointer(wfx + 24)) == 3
	}
	if channels < 1 || block < 1 || (isFloat && bits != 32) || (!isFloat && bits != 16 && bits != 24 && bits != 32) {
		return fmt.Errorf("unsupported sound format (%d channels, %d-bit)", channels, bits)
	}

	if hr := comCall(client, 3, 0 /*shared*/, flags, 2_000_000 /*200 ms*/, 0, wfx, 0); failed(hr) {
		return hresultError{"setting up sound capture", hr}
	}
	if hr := comCall(client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture))); failed(hr) {
		return hresultError{"getting the capture service", hr}
	}
	if hr := comCall(client, 10); failed(hr) {
		return hresultError{"starting sound capture", hr}
	}
	defer comCall(client, 11)

	sample := func(p uintptr) float32 {
		switch {
		case isFloat:
			return *(*float32)(unsafe.Pointer(p))
		case bits == 16:
			return float32(*(*int16)(unsafe.Pointer(p))) / 32768
		case bits == 24:
			b := (*[3]byte)(unsafe.Pointer(p))
			v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
			return float32(v) / 8388608
		default:
			return float32(*(*int32)(unsafe.Pointer(p))) / 2147483648
		}
	}
	bytesPer := bits / 8
	dec := Decimator{InRate: float64(rate)}
	var batch [][2]float32
	emit := func(l, r float32) { batch = append(batch, [2]float32{l, r}) }

	for {
		select {
		case <-stop:
			return nil
		case <-time.After(10 * time.Millisecond):
		}
		if flow == 0 && time.Since(lastCheck) > time.Second {
			lastCheck = time.Now()
			if following && defaultOutputID(enum) != startDefault {
				return errOutputChanged
			}
			if following && want != "" { // the chosen output may be back
				w, _ := syscall.UTF16PtrFromString(want)
				var d uintptr
				if hr := comCall(enum, 5, uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(&d))); !failed(hr) {
					var st uint32
					comCall(d, 6, uintptr(unsafe.Pointer(&st)))
					comRelease(d)
					if st == 1 {
						return errOutputChanged
					}
				}
			}
		}
		for {
			var packet uint32
			if hr := comCall(capture, 5, uintptr(unsafe.Pointer(&packet))); failed(hr) {
				return hresultError{"capturing sound", hr} // e.g. speakers unplugged
			}
			if packet == 0 {
				break
			}
			var data uintptr
			var frames, flags uint32
			if hr := comCall(capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)),
				uintptr(unsafe.Pointer(&flags)), 0, 0); failed(hr) {
				return hresultError{"capturing sound", hr}
			}
			silent := flags&0x2 != 0
			for i := 0; i < int(frames); i++ {
				var l, r float32
				if !silent {
					base := data + uintptr(i*block)
					l = sample(base)
					r = l
					if channels > 1 {
						r = sample(base + uintptr(bytesPer))
					}
				}
				dec.Push(l, r, emit)
			}
			comCall(capture, 4, uintptr(frames))
		}
		if len(batch) > 0 {
			out(batch)
			batch = nil
		}
	}
}
