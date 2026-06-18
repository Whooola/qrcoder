package main

import (
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
	// Find window by class name
	className, _ := windows.UTF16PtrFromString(mainWndClass)
	hwnd, _ := windows.FindWindow(className, nil)
	if hwnd != 0 {
		// Bring to foreground
		windows.SetForegroundWindow(hwnd)
		windows.ShowWindow(hwnd, windows.SW_RESTORE)
	}
}
