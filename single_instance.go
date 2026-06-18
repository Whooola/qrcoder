package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// Single instance protection via named mutex
// ────────────────────────────────────────────────────────────

const mutexName = "Global\\QRCoder_SingleInstance"

var instanceMutex windows.Handle

// acquireSingleInstance creates or opens a named mutex. Returns
// false if another instance is already running.
func acquireSingleInstance() bool {
	muName, _ := windows.UTF16PtrFromString(mutexName)

	handle, err := windows.CreateMutex(nil, true, muName)
	if err != nil {
		// If we can't create the mutex, allow the app to start
		return true
	}

	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(handle)
		// Find and bring existing instance window to foreground
		bringExistingToFront()
		return false
	}

	instanceMutex = handle
	return true
}

// releaseSingleInstance releases the instance mutex.
func releaseSingleInstance() {
	if instanceMutex != 0 {
		windows.CloseHandle(instanceMutex)
		instanceMutex = 0
	}
}

// bringExistingToFront finds the existing QRCoder window and
// brings it to the foreground.
func bringExistingToFront() {
	user32 := windows.NewLazySystemDLL("user32.dll")

	// Find window by class name
	className, _ := windows.UTF16PtrFromString(mainWndClass)
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(
		uintptr(unsafe.Pointer(className)),
		0,
	)
	if hwnd != 0 {
		// Bring to foreground
		user32.NewProc("SetForegroundWindow").Call(hwnd)
		user32.NewProc("ShowWindow").Call(hwnd, 9) // SW_RESTORE = 9
	}
}
