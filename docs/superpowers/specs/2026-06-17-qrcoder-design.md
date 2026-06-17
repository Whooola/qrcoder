# QRCoder Design Specification

**Date:** 2026-06-17
**Target:** Windows 7 SP1+, portable single exe

## Overview

QRCoder is a system-tray QR code utility. Double-tap Ctrl with text selected to generate a QR code; double-tap Ctrl with no selection to scan a QR code from camera.

## Technology Stack

| Choice | Decision |
|--------|----------|
| Language | Go |
| GUI | Native Win32 API (`golang.org/x/sys/windows`) |
| QR Generate | `rsc.io/qr` |
| QR Scan | Pure Go (`makiuchi-d/gozxing`), fallback to cgo-embedded zbar if needed |
| Hotkey | Low-level keyboard hook (`SetWindowsHookEx` WH_KEYBOARD_LL) |
| Camera | DirectShow / Windows Media Foundation via Win32 API |
| Build | Single exe, `-ldflags "-s -w -H windowsgui"`, icon via `rsrc` |

## File Structure

```
qrcoder/
├── main.go          ← Entry point, message loop, tray icon, menu
├── hotkey.go        ← Keyboard hook, double-Ctrl detection
├── qrcode.go        ← QR generation and recognition
├── camera.go        ← Camera access, frame capture
├── ui.go            ← Popup windows (QR display, scan preview)
├── go.mod
└── go.sum
```

5 Go source files, compile to a single `qrcoder.exe` (~3-6 MB).

## Component Design

### 1. Hotkey Detection (`hotkey.go`)

Global keyboard hook `WH_KEYBOARD_LL` monitors all key events. State machine:

```
IDLE → Ctrl↓ recorded
     → Ctrl↑ recorded (start 500ms timer)
       → Other key pressed before Ctrl↑ → reset to IDLE (was a shortcut)
       → Ctrl↓ again within 500ms and no other keys → TRIGGER
       → 500ms timeout → reset to IDLE
```

Key behaviors:
- Ctrl held + any other key (C, V, etc.) → classified as normal shortcut, no trigger
- Only isolated Ctrl presses count
- 500ms window between the two Ctrl press events
- Hook is non-blocking; does not interfere with other applications

### 2. QR Generation (`qrcode.go`)

**Flow:**
```
Input text
  → Check length (QR V40 max ~4296 alphanumeric chars)
    → Exceeds? Truncate, show warning
    → OK? Encode via rsc.io/qr (Level H, ~30% error correction)
  → Render to PNG with white margin
  → Display in popup window
```

**Popup window:**
- Default size 300×300 px (including margin)
- User can resize by dragging window border; QR scales proportionally (maintains square aspect ratio)
- Minimum 150×150 px
- Title shows character count, e.g. "共 128 字符" or "已截断，共 2953 字符"
- Right-click copies QR image to clipboard

### 3. QR Scanning (`camera.go` + `qrcode.go`)

**Flow:**
```
Open first available camera
  → Capture loop:
    → Read frame
    → Preprocess (grayscale, binarize)
    → Attempt QR detect (gozxing)
      → Found? → Stop camera, show result popup
      → Not found? → Continue next frame
  → Timeout 30s? → Close camera, show "未识别到二维码"
```

**Scan window:**
- Green guide frame centered on screen
- Hint text below: "请将二维码对准框内"
- On success: text changes to result, result copyable, URLs rendered as clickable links
- On close or success: camera released

### 4. System Tray (`main.go`)

- Tray icon embedded as `.syso` resource (via `rsrc`)
- Left double-click tray icon → trigger "generate QR" (read current selection)
- Notification icon added on startup, removed on exit

**Right-click menu:**
```
生成二维码    ← Manually trigger (reads selection)
扫描二维码    ← Open camera scan
────────────
√ 开机自启    ← Toggle, writes/removes registry Run key
────────────
使用说明      ← Simple usage popup
────────────
退出         ← Exit application
```

### 5. Auto-start (`main.go`)

Registry path: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
Key: `QRCoder` = `<exe path>`

- On check: write registry key
- On uncheck: delete registry key
- On startup: read registry to sync menu check state

### 6. Single Instance

Detect existing instance on startup via named mutex or window title. If already running, activate the existing instance window and exit the new one.

## Error Handling

| Scenario | Behavior |
|----------|----------|
| No camera detected | Popup: "未检测到摄像头" |
| Camera in use | Popup: "摄像头不可用" |
| Selection empty / non-text | No trigger (clipboard unchanged or non-text content) |
| Clipboard read failure | Silent ignore |
| QR generation failure | Popup with error |
| Scan timeout (30s) | Auto-close camera, "未识别到二维码" |
| System wake from sleep | Re-register hotkey hook |
| Non-text clipboard (image etc.) | Treated as empty → scan mode |

## Build & Distribution

```bash
# Generate .syso resource (icon)
rsrc -ico assets/icon.ico -o rsrc.syso

# Build
go build -ldflags "-s -w -H windowsgui" -o qrcoder.exe

# Result: single portable qrcoder.exe
```

## Testing

- Unit tests: QR encode correctness, text length truncation logic
- Manual tests: hotkey interaction, camera, UI across Win7/Win10/Win11
