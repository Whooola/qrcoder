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

// captureSelection gets the currently selected text from the foreground
// application. It first tries UI Automation (direct, no clipboard interference).
// Falls back to simulating Ctrl+C if UIA is unavailable.
func captureSelection() string {
	// Primary: UI Automation — directly reads selected text
	if text := captureByUIAutomation(); text != "" {
		return text
	}

	// Fallback: clipboard-based approach
	return captureByClipboard()
}

// captureByClipboard sends Ctrl+C to the foreground window and reads
// the resulting clipboard text. Returns empty string if no text was
// copied or the clipboard didn't change.
func captureByClipboard() string {
	// Save current clipboard content
	backup := backupClipboard()

	// Send Ctrl+C to the foreground window
	simulateCtrlC()

	// Wait for clipboard update (with retry logic)
	result := waitForClipboardChange(backup)

	// Restore original clipboard content
	restoreClipboard(backup)

	return result
}

// waitForClipboardChange waits for the clipboard to be updated with
// new text after the Ctrl+C simulation.
func waitForClipboardChange(backup clipboardBackup) string {
	// Try multiple times with increasing wait
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(30+10*i) * time.Millisecond)
		result := readClipboardText()
		if result != "" && result != backup.text {
			return result
		}
	}
	// Last attempt: just return whatever is there
	return readClipboardText()
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

	// Send each key event individually with small delays to mimic
	// real keyboard input timing. This is more reliable than sending
	// all events at once, which some applications may not process correctly.
	sendKey := func(vk uint16, scan uint16, flags uint32) bool {
		input := INPUT{
			Type: INPUT_KEYBOARD,
			Ki: KEYBDINPUT{
				WVk:         vk,
				WScan:       scan,
				DwFlags:     flags,
				DwExtraInfo: 0,
			},
		}
		ret, _, _ := sendInput.Call(
			uintptr(1),
			uintptr(unsafe.Pointer(&input)),
			uintptr(unsafe.Sizeof(INPUT{})),
		)
		return ret != 0
	}

	// Ctrl down
	sendKey(0x11, 0, KEYEVENTF_KEYDOWN)
	time.Sleep(5 * time.Millisecond)

	// C down
	sendKey(0x43, 0, KEYEVENTF_KEYDOWN)
	time.Sleep(10 * time.Millisecond)

	// C up
	sendKey(0x43, 0, KEYEVENTF_KEYUP)
	time.Sleep(5 * time.Millisecond)

	// Ctrl up
	sendKey(0x11, 0, KEYEVENTF_KEYUP)
	time.Sleep(10 * time.Millisecond)
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

