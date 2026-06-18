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
// application using a multi-layer approach. Each layer is tried in order;
// if one fails or crashes, the next is attempted. A recover() guard ensures
// no panic in any layer can break the message handler.
func captureSelection() (text string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("captureSelection panic recovered: %v", r)
			text = ""
		}
	}()

	// Layer 1: UI Automation — direct, no clipboard, most reliable
	if text = safeUIA(); text != "" {
		return text
	}

	// Layer 2: WM_COPY message — send copy command directly to focused control
	if text = captureByWMCopy(); text != "" {
		return text
	}

	// Layer 3: SendInput Ctrl+C — simulate keyboard, widest compatibility
	return captureByClipboard()
}

// safeUIA calls captureByUIAutomation with its own recover guard.
func safeUIA() (text string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("UIA panic recovered: %v", r)
			text = ""
		}
	}()
	return captureByUIAutomation()
}

// ────────────────────────────────────────────────────────────
// Layer 2: WM_COPY via AttachThreadInput + SendMessage
// ────────────────────────────────────────────────────────────

const (
	WM_COPY = 0x0301
)

// captureByWMCopy uses AttachThreadInput to attach our input queue
// to the foreground thread, then sends WM_COPY to the focused control.
// This is more reliable than simulating Ctrl+C because it bypasses
// keyboard input injection entirely.
func captureByWMCopy() string {
	user32 := windows.NewLazySystemDLL("user32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	// Get foreground window
	getForegroundWindow := user32.NewProc("GetForegroundWindow")
	fgHwnd, _, _ := getForegroundWindow.Call()
	if fgHwnd == 0 {
		return ""
	}

	// Get foreground thread ID
	getWindowThreadProcessId := user32.NewProc("GetWindowThreadProcessId")
	fgTid, _, _ := getWindowThreadProcessId.Call(fgHwnd, 0)

	// Get our thread ID
	getCurrentThreadId := kernel32.NewProc("GetCurrentThreadId")
	ourTid, _, _ := getCurrentThreadId.Call()

	if fgTid == ourTid {
		return "" // can't attach to our own thread
	}

	// Attach input queues
	attachThreadInput := user32.NewProc("AttachThreadInput")
	ret, _, _ := attachThreadInput.Call(ourTid, fgTid, 1) // TRUE
	if ret == 0 {
		return ""
	}
	defer attachThreadInput.Call(ourTid, fgTid, 0) // FALSE

	// Get the focused control in the foreground window
	getFocus := user32.NewProc("GetFocus")
	focusHwnd, _, _ := getFocus.Call()
	if focusHwnd == 0 {
		return ""
	}

	// Save clipboard, send WM_COPY, read result, restore
	backup := backupClipboard()

	sendMessageW := user32.NewProc("SendMessageW")
	sendMessageW.Call(focusHwnd, WM_COPY, 0, 0)

	result := waitForClipboardChange(backup)
	restoreClipboard(backup)

	return result
}

// ────────────────────────────────────────────────────────────
// Layer 3: SendInput Ctrl+C (fallback)
// ────────────────────────────────────────────────────────────

// captureByClipboard sends Ctrl+C to the foreground window and reads
// the resulting clipboard text.
func captureByClipboard() string {
	backup := backupClipboard()
	simulateCtrlC()
	result := waitForClipboardChange(backup)
	restoreClipboard(backup)
	return result
}

// waitForClipboardChange waits for the clipboard to be updated with
// new text after a copy command. Retries with increasing delays.
func waitForClipboardChange(backup clipboardBackup) string {
	for i := 0; i < 8; i++ {
		time.Sleep(time.Duration(20+10*i) * time.Millisecond)
		result := readClipboardText()
		if result != "" && result != backup.text {
			return result
		}
	}
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

	// Ctrl down (VK_CONTROL=0x11, scan=0x1D)
	sendKey(0x11, 0x1D, KEYEVENTF_KEYDOWN)
	time.Sleep(5 * time.Millisecond)

	// C down (VK_C=0x43, scan=0x2E)
	sendKey(0x43, 0x2E, KEYEVENTF_KEYDOWN)
	time.Sleep(15 * time.Millisecond)

	// C up
	sendKey(0x43, 0x2E, KEYEVENTF_KEYUP)
	time.Sleep(5 * time.Millisecond)

	// Ctrl up
	sendKey(0x11, 0x1D, KEYEVENTF_KEYUP)
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

