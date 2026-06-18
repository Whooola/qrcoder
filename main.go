package main

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// Win32 type aliases
// ────────────────────────────────────────────────────────────

type (
	HANDLE  = windows.Handle
	HINST   = windows.Handle
	HHOOK   = windows.Handle
	HMENU   = windows.Handle
	HBITMAP = windows.Handle
	HDC     = windows.Handle
	HGDIOBJ = windows.Handle
	WPARAM  = uintptr
	LPARAM  = uintptr
	LRESULT = uintptr
	DWORD   = uint32
	BOOL    = int32
	UINT    = uint32
	LONG    = int32
)

// ────────────────────────────────────────────────────────────
// Window class names
// ────────────────────────────────────────────────────────────

const (
	mainWndClass      = "QRCoder_MainWnd"
	qrPopupClass      = "QRCoder_QRPopup"
	scanPreviewClass  = "QRCoder_ScanPreview"
	resultPopupClass  = "QRCoder_ResultPopup"
)

// ────────────────────────────────────────────────────────────
// Window messages
// ────────────────────────────────────────────────────────────

const (
	WM_DESTROY       = 0x0002
	WM_PAINT         = 0x000F
	WM_CLOSE         = 0x0010
	WM_SIZE          = 0x0005
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONUP     = 0x0205
	WM_LBUTTONDBLCLK = 0x0203
	WM_COMMAND       = 0x0111
	WM_CREATE        = 0x0001
	WM_ERASEBKGND    = 0x0014
	WM_GETMINMAXINFO = 0x0024
	WM_NCHITTEST     = 0x0084
	WM_POWERBROADCAST = 0x0218

	// Custom message for tray icon callbacks
	WM_TRAYICON     = 0x0400 + 100 // WM_APP + 100
	WM_TRIGGER_GEN   = 0x0400 + 101 // double-Ctrl → generate
	WM_TRIGGER_SCAN  = 0x0400 + 102 // manual scan trigger
	WM_SCAN_RESULT   = 0x0400 + 103 // scan result ready (wParam=0 fail, lParam=text ptr)
	WM_CLOSE_PREVIEW = 0x0400 + 104 // close scan preview window
)

// ────────────────────────────────────────────────────────────
// Power broadcast constants
// ────────────────────────────────────────────────────────────

const PBT_APMRESUMEAUTOMATIC = 18

// ────────────────────────────────────────────────────────────
// Menu command IDs
// ────────────────────────────────────────────────────────────

const (
	IDM_GENERATE  = 1001
	IDM_SCAN      = 1002
	IDM_AUTOSTART = 1003
	IDM_ABOUT     = 1004
	IDM_EXIT      = 1005
)

// ────────────────────────────────────────────────────────────
// Tray icon flags
// ────────────────────────────────────────────────────────────

const (
	NIF_MESSAGE = 1
	NIF_ICON    = 2
	NIF_TIP     = 4

	NIM_ADD    = 0
	NIM_DELETE = 2
	NIM_MODIFY = 1
)

// ────────────────────────────────────────────────────────────
// Menu flags
// ────────────────────────────────────────────────────────────

const (
	MF_STRING    = 0x00000000
	MF_SEPARATOR = 0x00000800
	MF_CHECKED   = 0x00000008
)

// ────────────────────────────────────────────────────────────
// Standard icon IDs
// ────────────────────────────────────────────────────────────

const IDI_APPLICATION = 32512

// ────────────────────────────────────────────────────────────
// Window procedure type and registration
// ────────────────────────────────────────────────────────────

// WNDCLASSEXW mirrors the Win32 WNDCLASSEXW structure.
type WNDCLASSEXW struct {
	CbSize        UINT
	Style         UINT
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     HINST
	HIcon         HANDLE
	HCursor       HANDLE
	HbrBackground HANDLE
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       HANDLE
}

// registerWindowClass registers a window class with the given
// name and window procedure callback.
func registerWindowClass(className string, wndProc uintptr) error {
	hInst, err := windows.GetModuleHandle(nil)
	if err != nil {
		return fmt.Errorf("GetModuleHandle: %w", err)
	}

	cname, _ := syscall.UTF16PtrFromString(className)

	wc := WNDCLASSEXW{
		CbSize:        UINT(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         0,
		LpfnWndProc:   wndProc,
		CbClsExtra:    0,
		CbWndExtra:    0,
		HInstance:     HINST(hInst),
		HIcon:         0,
		HCursor:       0,
		HbrBackground: 0,
		LpszClassName: cname,
		HIconSm:       0,
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	regClass := user32.NewProc("RegisterClassExW")
	atom, _, _ := regClass.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW failed: %d", windows.GetLastError())
	}
	return nil
}

// createWindow creates a window of the given class.
func createWindow(className, windowName string, style uint32, x, y, w, h int32, parent HANDLE) (HANDLE, error) {
	hInst, err := windows.GetModuleHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("GetModuleHandle: %w", err)
	}

	cname, _ := syscall.UTF16PtrFromString(className)
	wname, _ := syscall.UTF16PtrFromString(windowName)

	user32 := windows.NewLazySystemDLL("user32.dll")
	createWin := user32.NewProc("CreateWindowExW")
	hwnd, _, _ := createWin.Call(
		0,                              // dwExStyle
		uintptr(unsafe.Pointer(cname)), // lpClassName
		uintptr(unsafe.Pointer(wname)), // lpWindowName
		uintptr(style),                 // dwStyle
		uintptr(x), uintptr(y),         // x, y
		uintptr(w), uintptr(h),         // width, height
		uintptr(parent),                // hWndParent
		0,                              // hMenu
		uintptr(hInst),                 // hInstance
		0,                              // lpParam
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW failed: %d", windows.GetLastError())
	}
	return HANDLE(hwnd), nil
}

// ────────────────────────────────────────────────────────────
// MSG structure and message loop
// ────────────────────────────────────────────────────────────

// MSG mirrors the Win32 MSG structure.
type MSG struct {
	HWnd    HANDLE
	Message UINT
	WParam  WPARAM
	LParam  LPARAM
	Time    DWORD
	Pt      struct{ X, Y int32 }
}

// ────────────────────────────────────────────────────────────
// Tray icon (NOTIFYICONDATA)
// ────────────────────────────────────────────────────────────

// NOTIFYICONDATAW mirrors the Win32 NOTIFYICONDATAW structure.
type NOTIFYICONDATAW struct {
	CbSize           DWORD
	HWnd             HANDLE
	UID              UINT
	UFlags           UINT
	UCallbackMessage UINT
	HIcon            HANDLE
	SzTip            [128]uint16
	DwState          DWORD
	DwStateMask      DWORD
	SzInfo           [256]uint16
	UVersion         UINT
	SzInfoTitle      [64]uint16
	DwInfoFlags      DWORD
	GuidItem         windows.GUID
	HBalloonIcon     HANDLE
}

// ────────────────────────────────────────────────────────────
// Global state
// ────────────────────────────────────────────────────────────

var (
	mainHWND        HANDLE
	scanPreviewHWND HANDLE
	activeCamera    *cameraSession
	activeCameraMu  sync.Mutex
	trayUID         UINT = 1
	trayMenu        HMENU
	autoStart       bool
	onDoubleCtrl    func() // set by main
)

// ────────────────────────────────────────────────────────────
// Tray icon management
// ────────────────────────────────────────────────────────────

func shellNotifyIcon(dwMessage DWORD, nid *NOTIFYICONDATAW) error {
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	shellNotify := shell32.NewProc("Shell_NotifyIconW")
	ret, _, _ := shellNotify.Call(uintptr(dwMessage), uintptr(unsafe.Pointer(nid)))
	if ret == 0 {
		return fmt.Errorf("Shell_NotifyIconW failed: %d", windows.GetLastError())
	}
	return nil
}

func addTrayIcon() error {
	iconHandle, err := loadAppIcon()
	if err != nil {
		// Fallback to standard application icon
		iconHandle, _ = loadStandardIcon(IDI_APPLICATION)
	}

	tip, _ := syscall.UTF16FromString("QRCoder - 二维码工具")

	nid := &NOTIFYICONDATAW{
		CbSize:           DWORD(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:             mainHWND,
		UID:              trayUID,
		UFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            iconHandle,
	}
	copy(nid.SzTip[:], tip)

	return shellNotifyIcon(NIM_ADD, nid)
}

func removeTrayIcon() {
	nid := &NOTIFYICONDATAW{
		CbSize: DWORD(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:   mainHWND,
		UID:    trayUID,
	}
	shellNotifyIcon(NIM_DELETE, nid)
}

func loadAppIcon() (HANDLE, error) {
	// Try resource ID 2 (the .syso icon)
	user32 := windows.NewLazySystemDLL("user32.dll")
	loadIcon := user32.NewProc("LoadIconW")
	handle, _, _ := loadIcon.Call(
		uintptr(windows.GetModuleHandle(nil)),
		uintptr(uint16(2)), // MAKEINTRESOURCE(2)
	)
	if handle == 0 {
		return 0, fmt.Errorf("LoadIconW failed for resource ID 2")
	}
	return HANDLE(handle), nil
}

func loadStandardIcon(id uint16) (HANDLE, error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	loadIcon := user32.NewProc("LoadIconW")
	handle, _, _ := loadIcon.Call(0, uintptr(id))
	if handle == 0 {
		return 0, fmt.Errorf("LoadIconW failed for standard icon %d", id)
	}
	return HANDLE(handle), nil
}

// ────────────────────────────────────────────────────────────
// Tray context menu
// ────────────────────────────────────────────────────────────

func createTrayMenu() HMENU {
	user32 := windows.NewLazySystemDLL("user32.dll")
	createMenu := user32.NewProc("CreatePopupMenu")
	menu, _, _ := createMenu.Call()
	if menu == 0 {
		return 0
	}

	appendMenu := user32.NewProc("AppendMenuW")
	appendStr := func(flags UINT, id uintptr, text string) {
		t, _ := syscall.UTF16PtrFromString(text)
		appendMenu.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(t)))
	}

	appendStr(MF_STRING, IDM_GENERATE, "生成二维码")
	appendStr(MF_STRING, IDM_SCAN, "扫描二维码")
	appendStr(MF_SEPARATOR, 0, "")
	checkFlag := UINT(0)
	if autoStart {
		checkFlag = MF_CHECKED
	}
	appendStr(MF_STRING|checkFlag, IDM_AUTOSTART, "开机自启")
	appendStr(MF_SEPARATOR, 0, "")
	appendStr(MF_STRING, IDM_ABOUT, "使用说明")
	appendStr(MF_SEPARATOR, 0, "")
	appendStr(MF_STRING, IDM_EXIT, "退出")

	return HMENU(menu)
}

func showTrayMenu() {
	user32 := windows.NewLazySystemDLL("user32.dll")

	// Recreate menu each time to reflect current autoStart state
	trayMenu = createTrayMenu()

	// Get cursor position
	var pt struct{ X, Y int32 }
	getCursorPos := user32.NewProc("GetCursorPos")
	getCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// SetForegroundWindow — required for TrackPopupMenu to work correctly
	setForeground := user32.NewProc("SetForegroundWindow")
	setForeground.Call(uintptr(mainHWND))

	// Show menu
	trackPopup := user32.NewProc("TrackPopupMenu")
	trackPopup.Call(
		uintptr(trayMenu),
		0,                   // TPM_LEFTALIGN | TPM_TOPALIGN
		uintptr(pt.X),
		uintptr(pt.Y),
		0,                   // nReserved
		uintptr(mainHWND),   // hWnd
		0,                   // prcRect
	)

	// Post benign message to dismiss menu properly
	postMsg := user32.NewProc("PostMessageW")
	postMsg.Call(uintptr(mainHWND), WM_NULL, 0, 0)
}

// ────────────────────────────────────────────────────────────
// Main window procedure
// ────────────────────────────────────────────────────────────

func mainWindowProc(hwnd HANDLE, msg UINT, wParam WPARAM, lParam LPARAM) LRESULT {
	switch msg {
	case WM_TRIGGER_GEN:
		handleGenerate()
		return 0

	case WM_TRIGGER_SCAN:
		handleScan()
		return 0

	case WM_SCAN_RESULT:
		// wParam: 0 = fail, 1 = success
		// lParam: pointer to result string (allocated via GlobalAlloc)
		handleScanResult(uint32(wParam), lParam)
		return 0

	case WM_CLOSE_PREVIEW:
		// Stop active camera and close preview window
		func() {
			activeCameraMu.Lock()
			defer activeCameraMu.Unlock()
			if activeCamera != nil {
				activeCamera.close()
				activeCamera = nil
			}
		}()
		if scanPreviewHWND != 0 {
			destroyWindow(scanPreviewHWND)
			scanPreviewHWND = 0
		}
		return 0

	case WM_TRAYICON:
		switch uint32(lParam) {
		case WM_LBUTTONDBLCLK:
			handleGenerate()
		case WM_RBUTTONUP:
			showTrayMenu()
		}
		return 0

	case WM_COMMAND:
		switch uint32(wParam) {
		case IDM_GENERATE:
			handleGenerate()
		case IDM_SCAN:
			handleScan()
		case IDM_AUTOSTART:
			toggleAutoStart()
		case IDM_ABOUT:
			showAboutDialog()
		case IDM_EXIT:
			shutdownApp()
		}
		return 0

	case WM_DESTROY:
		shutdownApp()
		return 0

	case WM_POWERBROADCAST:
		if uint32(wParam) == PBT_APMRESUMEAUTOMATIC {
			// Re-register keyboard hook after system wake
			unhookHotkey()
			setHotkeyHook()
		}
		return 1
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	defProc := user32.NewProc("DefWindowProcW")
	ret, _, _ := defProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return LRESULT(ret)
}

// ────────────────────────────────────────────────────────────
// Action handlers (called from message loop thread)
// ────────────────────────────────────────────────────────────

func handleGenerate() {
	text := captureSelection()
	if text == "" {
		// No text selected — switch to scan mode
		handleScan()
		return
	}
	pngBytes, truncated, err := generateQR(text)
	if err != nil {
		showError("生成失败", err.Error())
		return
	}
	showQRPopup(pngBytes, len([]rune(text)), truncated)
}

func handleScan() {
	// Only allow one scan session at a time
	if scanPreviewHWND != 0 {
		return
	}
	showScanPreview(&scanPreviewHWND)

	// Run camera capture in background goroutine.
	// Results are posted back via WM_SCAN_RESULT.
	go func() {
		result, err := scanFromCamera(&activeCamera)
		user32 := windows.NewLazySystemDLL("user32.dll")
		postMsg := user32.NewProc("PostMessageW")

		if err != nil {
			// Post failure: wParam=0
			postMsg.Call(uintptr(mainHWND), WM_SCAN_RESULT, 0, 0)
		} else {
			// Post success: wParam=1, lParam=pointer to result text
			textPtr := allocGlobalString(result)
			if textPtr == 0 {
				postMsg.Call(uintptr(mainHWND), WM_SCAN_RESULT, 0, 0)
			} else {
				postMsg.Call(uintptr(mainHWND), WM_SCAN_RESULT, 1, textPtr)
			}
		}
	}()
}

func handleScanResult(success uint32, lParam LPARAM) {
	// Clean up camera and preview window (protected by mutex)
	func() {
		activeCameraMu.Lock()
		defer activeCameraMu.Unlock()
		if activeCamera != nil {
			activeCamera.close()
			activeCamera = nil
		}
	}()
	if scanPreviewHWND != 0 {
		destroyWindow(scanPreviewHWND)
		scanPreviewHWND = 0
	}

	if success == 1 && lParam != 0 {
		result := readGlobalString(lParam)
		freeGlobalString(lParam)
		showScanResult(result)
	} else {
		showError("扫描失败", "未识别到二维码")
	}
}

// allocGlobalString allocates a GlobalAlloc buffer containing the
// given UTF-16 string. Used to pass strings between threads via
// PostMessage. Caller must call freeGlobalString.
func allocGlobalString(s string) uintptr {
	utf16, _ := syscall.UTF16FromString(s)
	size := len(utf16) * 2

	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	globalAlloc := kernel32.NewProc("GlobalAlloc")
	mem, _, _ := globalAlloc.Call(0, uintptr(size)) // GMEM_FIXED
	if mem == 0 {
		return 0
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(mem)), len(utf16))
	copy(dst, utf16)
	return mem
}

func readGlobalString(mem uintptr) string {
	// Walk the UTF-16 string to find its null terminator, then
	// create a correctly-sized slice instead of a huge unsafe one.
	const maxLen = 65536
	s := unsafe.Slice((*uint16)(unsafe.Pointer(mem)), maxLen)
	n := 0
	for n < maxLen && s[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(s[:n])
}

func freeGlobalString(mem uintptr) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	globalFree := kernel32.NewProc("GlobalFree")
	globalFree.Call(mem)
}

func toggleAutoStart() {
	autoStart = !autoStart
	if autoStart {
		writeAutoStart()
	} else {
		deleteAutoStart()
	}
	// Menu will be recreated next time it's shown
}

func showAboutDialog() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	msgBox := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("QRCoder")
	text, _ := syscall.UTF16PtrFromString(
		"QRCoder - 二维码工具\r\n\r\n" +
			"使用方法：\r\n" +
			"• 选中文字后双击 Ctrl → 生成二维码\r\n" +
			"• 无选中文字时双击 Ctrl → 扫描二维码\r\n\r\n" +
			"左键双击托盘图标等同于生成二维码。\r\n" +
			"右键托盘图标查看菜单。",
	)
	msgBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40) // MB_ICONINFORMATION
}

func showError(title, message string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	msgBox := user32.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(message)
	msgBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10) // MB_ICONERROR
}

func shutdownApp() {
	removeTrayIcon()
	unhookHotkey()
	releaseSingleInstance()
	shutdownCOM()
	windows.PostQuitMessage(0)
}

// ────────────────────────────────────────────────────────────
// Auto-start via registry
// ────────────────────────────────────────────────────────────

func writeAutoStart() {
	key, err := windows.RegCreateKeyEx(
		windows.HKEY_CURRENT_USER,
		windows.StringToUTF16Ptr(`Software\Microsoft\Windows\CurrentVersion\Run`),
		0, nil, windows.REG_OPTION_NON_VOLATILE, windows.KEY_SET_VALUE, nil,
	)
	if err != nil {
		log.Printf("RegCreateKeyEx failed: %v", err)
		return
	}
	defer windows.RegCloseKey(key)

	exePath, _ := windows.GetCurrentProcess().Exe()
	if exePath == "" {
		// Fallback: use GetModuleFileName
		exePath = getModuleFileName()
	}

	err = windows.RegSetValueEx(
		key,
		windows.StringToUTF16Ptr("QRCoder"),
		0, windows.REG_SZ,
		[]byte(exePath+"\x00"),
	)
	if err != nil {
		log.Printf("RegSetValueEx failed: %v", err)
		autoStart = false
	}
}

func deleteAutoStart() {
	subKey, _ := windows.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Run`)
	valName, _ := windows.UTF16PtrFromString("QRCoder")
	err := windows.RegDeleteKeyValue(
		windows.HKEY_CURRENT_USER,
		subKey,
		valName,
	)
	if err != nil {
		log.Printf("RegDeleteKeyValue failed: %v", err)
	}
}

func getModuleFileName() string {
	buf := make([]uint16, 260)
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getModuleFileName := kernel32.NewProc("GetModuleFileNameW")
	n, _, _ := getModuleFileName.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func checkAutoStart() bool {
	val, err := windows.RegGetString(
		windows.HKEY_CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		"QRCoder",
	)
	return err == nil && val != ""
}

// ────────────────────────────────────────────────────────────
// COM helpers (stubs expanded in camera tasks)
// ────────────────────────────────────────────────────────────

func initCOM() error {
	err := windows.CoInitializeEx(0, 2) // COINIT_APARTMENTTHREADED
	if err != nil {
		return fmt.Errorf("CoInitializeEx: %w", err)
	}
	return nil
}

func shutdownCOM() {
	windows.CoUninitialize()
}

// ────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────

func winMain() int {
	runtime.LockOSThread()

	if err := initCOM(); err != nil {
		log.Fatalf("COM init: %v", err)
	}

	// Single instance check
	if !acquireSingleInstance() {
		showError("QRCoder", "程序已在运行中")
		return 1
	}

	// Check auto-start state
	autoStart = checkAutoStart()

	// Register main window class
	mainProc := syscall.NewCallback(mainWindowProc)
	if err := registerWindowClass(mainWndClass, mainProc); err != nil {
		log.Fatalf("Window class: %v", err)
	}

	// Create hidden main window (message-only would be ideal but some
	// tray icon operations need a real window on older Windows)
	hwnd, err := createWindow(mainWndClass, "QRCoder", 0, 0, 0, 0, 0, 0)
	if err != nil {
		log.Fatalf("Main window: %v", err)
	}
	mainHWND = hwnd

	// Add tray icon
	if err := addTrayIcon(); err != nil {
		log.Fatalf("Tray icon: %v", err)
	}

	// Wire double-Ctrl callback — post message to main window
	// instead of calling handler directly, so all UI operations
	// happen on the message loop thread.
	onDoubleCtrl = func() {
		user32 := windows.NewLazySystemDLL("user32.dll")
		postMsg := user32.NewProc("PostMessageW")
		postMsg.Call(uintptr(mainHWND), WM_TRIGGER_GEN, 0, 0)
	}

	// Set keyboard hook
	if err := setHotkeyHook(); err != nil {
		log.Printf("Warning: keyboard hook failed: %v", err)
		// Continue anyway — tray menu still works
	}

	log.Println("QRCoder started")

	// Message loop
	var msg MSG
	user32 := windows.NewLazySystemDLL("user32.dll")
	getMessage := user32.NewProc("GetMessageW")
	for {
		ret, _, _ := getMessage.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 { // WM_QUIT
			break
		}
		if int32(ret) == -1 {
			log.Printf("GetMessage error: %d", windows.GetLastError())
			break
		}
		translateMsg := user32.NewProc("TranslateMessage")
		translateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMsg := user32.NewProc("DispatchMessageW")
		dispatchMsg.Call(uintptr(unsafe.Pointer(&msg)))
	}

	log.Println("QRCoder exited")
	return 0
}

// main is the entry point. On Windows GUI subsystem, we use WinMain
// semantics — the CRT calls main, which calls our winMain.
func main() {
	osExit(winMain())
}

//go:noinline
func osExit(code int) { syscall.Exit(code) }
