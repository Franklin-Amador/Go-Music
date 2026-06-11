//go:build windows

package mediakeys

// mediakeys_windows.go — system-wide media-key support via Win32 RegisterHotKey.
//
// Hooks the four standard multimedia virtual-keys (VK_MEDIA_PLAY_PAUSE,
// VK_MEDIA_NEXT_TRACK, VK_MEDIA_PREV_TRACK, VK_MEDIA_STOP) so the user can
// pause/skip with the dedicated keys on their keyboard or with the Play/Pause
// button on Bluetooth headphones (which Windows synthesises as the same VK
// codes when no SMTC owner is active).
//
// Implementation notes:
//   - RegisterHotKey delivers WM_HOTKEY only to the THREAD that called it,
//     and only via its message queue. So we lock a goroutine to an OS thread,
//     register every hotkey on that thread, and pump GetMessage in a loop.
//   - We do NOT pass a window handle (the HWND arg is 0) — for a console-style
//     thread that's the documented way to receive hotkey messages without
//     needing a real window.
//   - The callbacks are dispatched directly from the message-pump goroutine.
//     Callers that need a specific thread (e.g. a UI thread) must marshal in
//     their handler (Fyne callers wrap in fyne.Do; the Wails app calls the
//     same goroutine-safe engine/playlist methods its JS binds use).

import (
	"log"
	"runtime"
	"syscall"
	"unsafe"
)

// Win32 constants — kept local rather than pulling in golang.org/x/sys/windows
// for just four values.
const (
	modNoRepeat = 0x4000

	vkMediaPlayPause = 0xB3
	vkMediaNextTrack = 0xB0
	vkMediaPrevTrack = 0xB1
	vkMediaStop      = 0xB2

	wmHotkey = 0x0312

	hkIDPlayPause = 1
	hkIDNext      = 2
	hkIDPrev      = 3
	hkIDStop      = 4
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procGetMessage       = user32.NewProc("GetMessageW")
)

// Win32 POINT and MSG structs — exactly the layout GetMessageW expects.
type win32Point struct{ X, Y int32 }

type win32Msg struct {
	HWND    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      win32Point
}

// Handler is the contract the caller passes to Start. Any of the fields may
// be nil; missing handlers are simply ignored when the corresponding key fires.
type Handler struct {
	OnPlayPause func()
	OnNext      func()
	OnPrev      func()
	OnStop      func()
}

// Start spawns a dedicated message-pump goroutine that registers the four
// media keys and dispatches WM_HOTKEY messages to the handler.
// Errors are logged but never fatal — if hotkey registration fails (e.g.
// another app already grabbed the key), the player still works, just
// without that particular key.
func Start(h Handler) {
	go func() {
		// RegisterHotKey + GetMessage are thread-bound. Pinning the goroutine
		// to a single OS thread is mandatory: messages registered on one
		// thread can't be retrieved by another.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		registerHotKey(hkIDPlayPause, vkMediaPlayPause)
		registerHotKey(hkIDNext, vkMediaNextTrack)
		registerHotKey(hkIDPrev, vkMediaPrevTrack)
		registerHotKey(hkIDStop, vkMediaStop)

		var m win32Msg
		for {
			ret, _, _ := procGetMessage.Call(
				uintptr(unsafe.Pointer(&m)), 0, 0, 0,
			)
			// GetMessage returns 0 on WM_QUIT and -1 on error. The thread
			// lives until the process exits, so we just bail out either way.
			if int32(ret) <= 0 {
				return
			}
			if m.Message != wmHotkey {
				continue
			}
			switch m.WParam {
			case hkIDPlayPause:
				if h.OnPlayPause != nil {
					h.OnPlayPause()
				}
			case hkIDNext:
				if h.OnNext != nil {
					h.OnNext()
				}
			case hkIDPrev:
				if h.OnPrev != nil {
					h.OnPrev()
				}
			case hkIDStop:
				if h.OnStop != nil {
					h.OnStop()
				}
			}
		}
	}()
}

func registerHotKey(id, vk uint32) {
	ret, _, err := procRegisterHotKey.Call(
		0,                    // hWnd (null = post to thread queue)
		uintptr(id),          // identifier
		uintptr(modNoRepeat), // modifiers — no repeat means a held key fires once
		uintptr(vk),          // virtual key
	)
	if ret == 0 {
		log.Printf("media keys: RegisterHotKey(VK=0x%02X) failed: %v", vk, err)
	}
}
