# QRCoder Implementation Plan

**Goal:** Build a Windows system-tray QR code utility — double-Ctrl to generate/scan QR codes, single portable exe, Win7 SP1+.

**Architecture:** Single Go package, 5 source files + 1 C trampoline. Hidden Win32 message window drives tray icon, keyboard hook, and popup UI. QR generation via `rsc.io/qr`, scanning via DirectShow COM with `go-ole`, keyboard hook via WH_KEYBOARD_LL with cgo trampoline.

**Tech Stack:** Go 1.21, `rsc.io/qr`, `go-ole`, `gozxing`, `golang.org/x/sys/windows`

## Global Constraints (from spec)
- Windows 7 SP1+
- Portable single exe, no installer
- Local-only, no network
- Native Win32 GUI, no webview
- Tray icon with right-click menu
- 500ms double-Ctrl window, must not trigger on Ctrl+C/V

---

## Phase 0: Project Scaffolding

### Task 0-1: Go module and dependencies
**Files:** `go.mod`, `go.sum`

```bash
cd /home/moletato/code/qrcoder
go mod init qrcoder
go get rsc.io/qr@latest
go get github.com/go-ole/go-ole@latest
go get github.com/makiuchi-d/gozxing@latest
go get golang.org/x/sys@latest
```

Verify: `go mod tidy` succeeds, `go build .` compiles empty package.

### Task 0-2: Icon resource
**Files:** `assets/icon.ico`, `rsrc.syso`

Generate a placeholder 32×32 icon. At minimum, create a stub that won't crash:
```bash
mkdir -p assets
# Place any valid .ico file at assets/icon.ico (can be a minimal generated one)
go install github.com/akavel/rsrc@latest
rsrc -ico assets/icon.ico -o rsrc.syso
```

Verify: `ls -l rsrc.syso` shows a non-empty file.

---

## Phase 1: Core Infrastructure

### Task 1: main.go — Window class, message loop, tray icon, menu dispatch
**File:** `main.go` (create, ~300 lines)

Contains:
- Win32 type aliases and window message constants
- `WNDCLASSEX` struct and `RegisterClassEx` call
- Hidden main window creation (`CreateWindowEx`)
- `MSG` struct and `GetMessage`/`DispatchMessage` loop
- `NOTIFYICONDATA` struct and `Shell_NotifyIcon` tray icon add/remove
- Tray context menu: CreatePopupMenu, AppendMenu (生成二维码, 扫描二维码, separator, 开机自启, separator, 使用说明, separator, 退出)
- `mainWindowProc` callback dispatching `WM_TRAYICON` (double-click → generate, right-click → show menu), `WM_COMMAND` (menu command IDs), `WM_DESTROY`
- `main()`: `runtime.LockOSThread()`, init COM, register class, create window, add tray, message loop, cleanup
- Stub handlers: `handleGenerate()`, `handleScan()`, `toggleAutoStart()`, `showAboutDialog()`

Win32 APIs: `RegisterClassExW`, `CreateWindowExW`, `GetMessageW`, `DispatchMessageW`, `Shell_NotifyIconW`, `CreatePopupMenu`, `AppendMenuW`, `GetCursorPos`, `SetForegroundWindow`, `TrackPopupMenu`, `DefWindowProcW`, `CoInitializeEx`, `CoUninitialize`

Verify: Program runs, tray icon appears, right-click shows menu. Exit via menu works.

---

## Phase 2: QR Generation

### Task 2: qrcode.go — Generate QR PNG from text
**File:** `qrcode.go` (create, ~80 lines)

```go
// generateQR encodes text as QR code (Level H), returns PNG bytes.
// Returns (pngBytes, wasTruncated, error).
// Truncation: byte-mode max 1273 chars (V40-H), alphanumeric max 1852.
func generateQR(text string) ([]byte, bool, error)

// truncateText truncates to the QR V40-H capacity limit.
func truncateText(text string) string

// needsTruncation checks if text exceeds QR capacity.
func needsTruncation(text string) bool
```

Implementation: `rsc.io/qr` encoding → scale modules to target 300px → render with 2-module white border → encode as PNG.

### Task 3: qrcode_test.go — Unit tests for generation
**File:** `qrcode_test.go` (create, ~40 lines)

Test cases:
- Short ASCII text → valid PNG, not truncated
- Text over 1273 bytes → truncated
- Empty string → error
- Unicode text → valid PNG
- Verify PNG output decodes as valid image

Verify: `go test ./...` passes.

---

## Phase 3: Keyboard Hook

### Task 4: hook.c — C trampoline for WH_KEYBOARD_LL callback
**File:** `hook.c` (create, ~20 lines)

Windows requires the `LowLevelKeyboardProc` to be a real C function (Go cannot provide a valid callback for this hook type). The C trampoline calls back into exported Go functions:

```c
#include <windows.h>

extern void goKeyboardEvent(DWORD vkCode, DWORD scanCode, DWORD flags, DWORD time, ULONG_PTR dwExtraInfo);

LRESULT CALLBACK LowLevelKeyboardProc(int nCode, WPARAM wParam, LPARAM lParam) {
    if (nCode == HC_ACTION) {
        KBDLLHOOKSTRUCT *kb = (KBDLLHOOKSTRUCT *)lParam;
        goKeyboardEvent(kb->vkCode, kb->scanCode, kb->flags, kb->time, kb->dwExtraInfo);
    }
    return CallNextHookEx(NULL, nCode, wParam, lParam);
}
```

### Task 5: hotkey.go — Double-Ctrl state machine
**File:** `hotkey.go` (create, ~100 lines)

```go
// Exported Go functions called from C trampoline:
//export goKeyboardEvent
func goKeyboardEvent(vkCode DWORD, scanCode DWORD, flags DWORD, time DWORD, dwExtraInfo uintptr)

// Hook management:
func setHotkeyHook() error     // SetWindowsHookEx(WH_KEYBOARD_LL, ...)
func unhookHotkey()            // UnhookWindowsHookEx

// State machine state:
// IDLE → FIRST_DOWN → FIRST_UP (start 500ms timer) → SECOND_DOWN → TRIGGER
// Any other key during FIRST_DOWN → reset to IDLE (was Ctrl+C/V/etc.)
```

Cgo setup in `hotkey.go`:
```go
/*
#cgo LDFLAGS: -luser32
#include <windows.h>
extern LRESULT CALLBACK LowLevelKeyboardProc(int nCode, WPARAM wParam, LPARAM lParam);
*/
import "C"
```

Double-Ctrl state machine uses `time.Now()` for timestamps and `sync.Mutex` for the state. The trigger callback is set via `var onDoubleCtrl func()`.

Verify: Run in console mode, double-tap Ctrl logs "triggered". Ctrl+C/V do not trigger.

---

## Phase 4: UI Popup Windows

### Task 6: ui.go — QR display popup
**File:** `ui.go` (create, ~200 lines)

- Register window classes: `qrPopupClass`, `scanPreviewClass`, `resultPopupClass`
- `showQRPopup(pngBytes []byte, charCount int, wasTruncated bool)`: 
  - Create window 300×300, title shows char count
  - Resizable, min 150×150, maintains square aspect ratio (handled in WM_SIZE/WM_GETMINMAXINFO)
  - WM_PAINT: StretchBlt the QR PNG to fill client area, keeping square proportions
  - Right-click context menu: "复制图片" (copy QR to clipboard)
- `showScanPreview()`: Camera preview window with green guide frame (GDI rectangle), hint text
- `showScanResult(text string)`: Result popup with copyable text, URL detection for clickable links

Win32 APIs: `RegisterClassExW`, `CreateWindowExW`, `BeginPaint`, `EndPaint`, `StretchBlt`, `CreateCompatibleDC`, `CreateCompatibleBitmap`, `GetClientRect`, `CreateRectRgn`, `FrameRect`, `DrawTextW`, `OpenClipboard`, `EmptyClipboard`, `SetClipboardData`, `CloseClipboard`

Verify: Manual test — call `showQRPopup` with generated PNG, window appears, resizable, image scales.

---

## Phase 5: Clipboard Text Capture

### Task 7: clipboard.go — Read text selection via Ctrl+C simulation
**File:** `clipboard.go` (create, ~60 lines)

```go
// captureSelection simulates Ctrl+C and reads clipboard text.
// Returns empty string if no text selection or copy failed.
func captureSelection() string
```

Flow:
1. Save current clipboard content
2. Send Ctrl+C (keybd_event or SendInput)
3. Wait 50ms for clipboard update
4. OpenClipboard → GetClipboardData(CF_UNICODETEXT) → read text
5. Restore original clipboard content
6. Return text (empty if unchanged or non-text)

Win32 APIs: `keybd_event` (or `SendInput`), `OpenClipboard`, `GetClipboardData`, `CloseClipboard`, `GlobalLock`, `GlobalUnlock`

Verify: Select text in Notepad, call `captureSelection()`, returns selected text.

---

## Phase 6: Camera & QR Scanning

### Task 8: camera.go — DirectShow camera access via go-ole
**File:** `camera.go` (create, ~300 lines)

This is the most complex file. Uses DirectShow COM interfaces via `go-ole`:

1. **Device enumeration**: `CoCreateInstance(CLSID_SystemDeviceEnum)` → `IEnumMoniker` → iterate devices, find first video capture device
2. **Filter graph**: `CoCreateInstance(CLSID_FilterGraph)` → `IGraphBuilder`
3. **Add source filter**: `IMoniker::BindToObject` for the selected device → add to graph
4. **Add Sample Grabber**: `CoCreateInstance(CLSID_SampleGrabber)` → configure media type (RGB24, 640×480) → add to graph
5. **Add Null Renderer**: `CoCreateInstance(CLSID_NullRenderer)` → add to graph
6. **Connect pins**: `ICaptureGraphBuilder2::RenderStream`
7. **Set callback**: `ISampleGrabber::SetCallback` — needs an `ISampleGrabberCB` implementation in Go
8. **Run/Stop**: `IMediaControl::Run`, `IMediaControl::Stop`

Key interfaces defined as Go structs mirroring COM vtables:
- `IGraphBuilder`, `ICaptureGraphBuilder2`, `IMediaControl`, `ISampleGrabber`, `IEnumMoniker`, `IMoniker`, `IPropertyBag`

### Task 9: camera.go — ISampleGrabberCB implementation and frame decode
**File:** `camera.go` (add to existing file, ~100 lines)

Implement `ISampleGrabberCB` in Go using `go-ole`'s COM object pattern. On each sample:
1. Copy RGB24 buffer from sample
2. Send to QR detector (call `detectQRFromFrame` in qrcode.go)
3. If QR found, signal via channel → main loop stops camera and shows result
4. 30s timeout via `time.After`

### Task 10: qrcode.go — QR detection from camera frame
**File:** `qrcode.go` (add ~40 lines)

```go
// detectQRFromFrame tries to find and decode a QR code in an RGB24 frame.
func detectQRFromFrame(rgbData []byte, width, height int) (string, error)
```

Convert RGB24 to grayscale `image.Gray`, then pass to `gozxing` decoder. Returns decoded text or error.

Verify: Manual test — point camera at a QR code, scan window shows result.

---

## Phase 7: Integration & Polish

### Task 11: Wire everything together
**File:** `main.go` (update stubs, ~80 lines)

- `handleGenerate()`: call `captureSelection()` → if text: `generateQR` → `showQRPopup`. If empty: call `handleScan()`
- `handleScan()`: call `showScanPreview()` → `openCamera()` → scan loop → on detect: close camera, `showScanResult()`. On timeout: close camera, show "未识别到二维码"
- `toggleAutoStart()`: write/delete `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\QRCoder`
- `showAboutDialog()`: MessageBox with usage instructions
- Wire `onDoubleCtrl` callback to `handleGenerate` in `main()`
- Re-register hook on system wake (`WM_POWERBROADCAST` → `PBT_APMRESUMEAUTOMATIC`)

### Task 12: Single instance and error handling
**File:** `main.go` (add ~30 lines)

- Single instance: `CreateMutex` with name "QRCoder_SingleInstance" on startup; if `GetLastError() == ERROR_ALREADY_EXISTS`, find and focus existing window, exit
- Error handling sweep: ensure every error path has a user-visible message (MessageBox for critical, silent for non-critical)
- Clipboard save/restore wrapped in defer

### Task 13: Build script and final compilation
**File:** `build.bat` (create, ~10 lines)

```bat
@echo off
rsrc -ico assets/icon.ico -o rsrc.syso
set CGO_ENABLED=1
go build -ldflags "-s -w -H windowsgui" -o qrcoder.exe
echo Build complete: qrcoder.exe
```

Verify: `qrcoder.exe` runs silently in tray. Double-Ctrl generates/scans. Menu works. Single exe, portable.

---

## Verification Checklist

1. **Build**: `build.bat` produces `qrcoder.exe` (~3-8 MB)
2. **Tray**: Icon appears, right-click menu shows all items
3. **Generate**: Select text in any app → double-Ctrl → QR popup appears, resizable, right-click copy
4. **Scan**: Deselect all text → double-Ctrl → camera opens with green guide → QR detected → result shown
5. **No false trigger**: Ctrl+C, Ctrl+V, Ctrl+Z do NOT trigger (verified with rapid copying)
6. **Auto-start**: Toggle menu item writes/deletes registry key
7. **Single instance**: Second launch activates existing tray icon, exits
8. **Error cases**: No camera → message shown. Scan timeout 30s → message shown
9. **Win7**: Verify on Win7 SP1+ (no APIs newer than Win7 used)
