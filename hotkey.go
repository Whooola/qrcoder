package main

/*
#cgo LDFLAGS: -luser32

#include <windows.h>

// Declare the C function from hook.c
extern LRESULT CALLBACK LowLevelKeyboardProc(int nCode, WPARAM wParam, LPARAM lParam);
*/
import "C"
import (
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// Double-Ctrl state machine
// ────────────────────────────────────────────────────────────

// States for the double-Ctrl detector.
type ctrlState int

const (
	ctrlIdle     ctrlState = iota // waiting for first Ctrl press
	ctrlFirstDown                  // first Ctrl is held down
	ctrlFirstUp                    // first Ctrl released, in 500ms window
)

const (
	ctrlVK     = 0x11 // VK_CONTROL
	doubleCtrlWindow = 500 * time.Millisecond
)

var (
	ctrlMu       sync.Mutex
	ctrlSt       = ctrlIdle
	ctrlFirstUpAt time.Time
	otherKeyDown bool // true if a non-Ctrl key was pressed during Ctrl hold
)

// ────────────────────────────────────────────────────────────
// Exported Go function called from C trampoline (hook.c)
// ────────────────────────────────────────────────────────────

//export goKeyboardEvent
func goKeyboardEvent(vkCode C.DWORD, scanCode C.DWORD, flags C.DWORD, dwTime C.DWORD, dwExtraInfo C.ULONG_PTR) {
	// flags bits 4-5 = LLKHF_INJECTED | LLKHF_LOWER_IL_INJECTED
	if (uint32(flags) & 0x30) != 0 {
		return
	}

	// flags bit 7 = transition state (1 = key release, 0 = key press)
	isKeyUp := (uint32(flags) & 0x80) != 0
	vk := uint32(vkCode)

	// Hold the mutex for ALL state access to avoid data races.
	ctrlMu.Lock()
	defer ctrlMu.Unlock()

	if vk != ctrlVK {
		if ctrlSt == ctrlFirstDown && !isKeyUp {
			otherKeyDown = true
		}
		return
	}

	if isKeyUp {
		switch ctrlSt {
		case ctrlFirstDown:
			if otherKeyDown {
				ctrlSt = ctrlIdle
				otherKeyDown = false
			} else {
				ctrlSt = ctrlFirstUp
				ctrlFirstUpAt = time.Now()
			}
		default:
			ctrlSt = ctrlIdle
		}
	} else {
		switch ctrlSt {
		case ctrlIdle:
			ctrlSt = ctrlFirstDown
			otherKeyDown = false
		case ctrlFirstUp:
			if time.Since(ctrlFirstUpAt) <= doubleCtrlWindow {
				ctrlSt = ctrlIdle
				log.Println("double-Ctrl detected")
				if onDoubleCtrl != nil {
					go onDoubleCtrl()
				}
			} else {
				ctrlSt = ctrlFirstDown
				otherKeyDown = false
			}
		default:
			ctrlSt = ctrlFirstDown
			otherKeyDown = false
		}
	}
}

// ────────────────────────────────────────────────────────────
// Hook management
// ────────────────────────────────────────────────────────────

var (
	hookHandle windows.Handle
	hookMu     sync.Mutex
)

const (
	WH_KEYBOARD_LL = 13
)

func setHotkeyHook() error {
	hookMu.Lock()
	defer hookMu.Unlock()

	if hookHandle != 0 {
		return nil // already set
	}

	// Load user32.dll for SetWindowsHookExW
	user32 := windows.NewLazySystemDLL("user32.dll")
	setHook := user32.NewProc("SetWindowsHookExW")

	// Get module handle for the current process (NULL for WH_KEYBOARD_LL)
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	hMod, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)

	// Get the C function pointer for LowLevelKeyboardProc
	hookProc := C.LowLevelKeyboardProc

	handle, _, lastErr := setHook.Call(
		WH_KEYBOARD_LL,
		uintptr(unsafe.Pointer(hookProc)),
		uintptr(hMod),
		0, // dwThreadId = 0 for global hook
	)
	if handle == 0 {
		return error(syscall.Errno(lastErr.(syscall.Errno)))
	}

	hookHandle = windows.Handle(handle)
	log.Println("Keyboard hook installed")
	return nil
}

func unhookHotkey() {
	hookMu.Lock()
	defer hookMu.Unlock()

	if hookHandle == 0 {
		return
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	unhook := user32.NewProc("UnhookWindowsHookEx")
	unhook.Call(uintptr(hookHandle))
	hookHandle = 0
	log.Println("Keyboard hook removed")
}
