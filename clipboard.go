package main

import (
	"log"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// Clipboard text capture via simulated Ctrl+C
// ────────────────────────────────────────────────────────────

const (
	CF_UNICODETEXT = 13
	CF_TEXT        = 1
)

// captureSelection sends Ctrl+C to the foreground window and reads
// the resulting clipboard text. Returns empty string if no text was
// copied or the clipboard didn't change.
func captureSelection() string {
	// Save current clipboard content
	backup := backupClipboard()

	// Send Ctrl+C to the foreground window
	simulateCtrlC()

	// Wait for clipboard update
	time.Sleep(70 * time.Millisecond)

	// Read clipboard text
	result := readClipboardText()

	// Restore original clipboard content
	restoreClipboard(backup)

	return result
}

// clipboardBackup holds saved clipboard text content.
type clipboardBackup struct {
	hasText bool
	text    string
}

func backupClipboard() clipboardBackup {
	backup := clipboardBackup{}

	user32 := windows.NewLazySystemDLL("user32.dll")

	openClipboard := user32.NewProc("OpenClipboard")
	ret, _, _ := openClipboard.Call(0)
	if ret == 0 {
		return backup
	}
	defer user32.NewProc("CloseClipboard").Call()

	getClipboardData := user32.NewProc("GetClipboardData")
	handle, _, _ := getClipboardData.Call(CF_UNICODETEXT)
	if handle != 0 {
		// Lock and read the text
		kernel32 := windows.NewLazySystemDLL("kernel32.dll")
		globalLock := kernel32.NewProc("GlobalLock")
		ptr, _, _ := globalLock.Call(handle)
		if ptr != 0 {
			backup.text = lpwstrToString((*uint16)(unsafe.Pointer(ptr)))
			backup.hasText = true
			kernel32.NewProc("GlobalUnlock").Call(handle)
		}
	}
	return backup
}

func restoreClipboard(backup clipboardBackup) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	openClipboard := user32.NewProc("OpenClipboard")
	ret, _, _ := openClipboard.Call(0)
	if ret == 0 {
		return
	}
	defer user32.NewProc("CloseClipboard").Call()

	user32.NewProc("EmptyClipboard").Call()

	if backup.hasText {
		text, _ := syscall.UTF16FromString(backup.text)
		byteLen := len(text) * 2 // UTF-16 = 2 bytes per char

		globalAlloc := kernel32.NewProc("GlobalAlloc")
		mem, _, _ := globalAlloc.Call(0x2002, uintptr(byteLen)) // GMEM_MOVEABLE = 0x0002
		if mem != 0 {
			globalLock := kernel32.NewProc("GlobalLock")
			ptr, _, _ := globalLock.Call(mem)
			if ptr != 0 {
				// Copy text to global memory
				dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(text))
				copy(dst, text)
				kernel32.NewProc("GlobalUnlock").Call(mem)
			}
			user32.NewProc("SetClipboardData").Call(CF_UNICODETEXT, mem)
		}
	}
}

// INPUT structure for SendInput
type KEYBDINPUT struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type INPUT struct {
	Type uint32
	Ki   KEYBDINPUT
	_    [8]byte // padding for MOUSEINPUT/HARDWAREINPUT union
}

const (
	INPUT_KEYBOARD    = 1
	KEYEVENTF_KEYDOWN = 0x0000
	KEYEVENTF_KEYUP   = 0x0002
)

func simulateCtrlC() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendInput := user32.NewProc("SendInput")

	inputs := [4]INPUT{
		// Ctrl down
		{Type: INPUT_KEYBOARD, Ki: KEYBDINPUT{WVk: 0x11, WScan: 0x1D, DwFlags: 0}},
		// C down
		{Type: INPUT_KEYBOARD, Ki: KEYBDINPUT{WVk: 0x43, WScan: 0x2E, DwFlags: 0}},
	}

	// Send key down events
	sendInput.Call(
		uintptr(2),
		uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(unsafe.Sizeof(INPUT{})),
	)

	// Brief pause for foreground app to register the Ctrl+C state
	time.Sleep(10 * time.Millisecond)

	// C up
	inputs[2] = INPUT{Type: INPUT_KEYBOARD, Ki: KEYBDINPUT{WVk: 0x43, WScan: 0x2E, DwFlags: KEYEVENTF_KEYUP}}
	// Ctrl up
	inputs[3] = INPUT{Type: INPUT_KEYBOARD, Ki: KEYBDINPUT{WVk: 0x11, WScan: 0x1D, DwFlags: KEYEVENTF_KEYUP}}

	sendInput.Call(
		uintptr(2),
		uintptr(unsafe.Pointer(&inputs[2])),
		uintptr(unsafe.Sizeof(INPUT{})),
	)
}

func readClipboardText() string {
	user32 := windows.NewLazySystemDLL("user32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	// Retry loop — clipboard may be locked briefly
	for i := 0; i < 10; i++ {
		openClipboard := user32.NewProc("OpenClipboard")
		ret, _, _ := openClipboard.Call(0)
		if ret != 0 {
			defer user32.NewProc("CloseClipboard").Call()

			getClipboardData := user32.NewProc("GetClipboardData")
			handle, _, _ := getClipboardData.Call(CF_UNICODETEXT)
			if handle != 0 {
				globalLock := kernel32.NewProc("GlobalLock")
				ptr, _, _ := globalLock.Call(handle)
				if ptr != 0 {
					text := lpwstrToString((*uint16)(unsafe.Pointer(ptr)))
					kernel32.NewProc("GlobalUnlock").Call(handle)
					return text
				}
			}
			return "" // Clipboard open but no text
		}
		time.Sleep(10 * time.Millisecond)
	}
	log.Println("Failed to open clipboard after retries")
	return ""
}

// lpwstrToString converts a null-terminated UTF-16 string to a Go string.
func lpwstrToString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}
	const maxLen = 65536
	s := unsafe.Slice(ptr, maxLen)
	n := 0
	for n < maxLen && s[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(s[:n])
}

