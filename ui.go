package main

import (
	"bytes"
	"fmt"
	"image/png"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// UI globals — per-window data tracked via GWLP_USERDATA
// ────────────────────────────────────────────────────────────

const GWLP_USERDATA = -21

// uiData keeps Go references to per-window data structs alive, preventing
// GC from collecting them while they are referenced only by uintptr in GWLP_USERDATA.
var (
	uiDataMu sync.Mutex
	uiData   = map[HANDLE]interface{}{}
)

type qrPopupData struct {
	pngBytes    []byte
	charCount   int
	truncated   bool
	hBitmap     HBITMAP
	imgWidth    int
	imgHeight   int
	origWidth   int
	origHeight  int
}

type scanResultData struct {
	resultText string
}

// ────────────────────────────────────────────────────────────
// Window procedures
// ────────────────────────────────────────────────────────────

var (
	qrProcVar     uintptr
	scanProcVar   uintptr
	resultProcVar uintptr
)

func init() {
	qrProcVar = syscall.NewCallback(qrPopupWndProc)
	scanProcVar = syscall.NewCallback(scanPreviewWndProc)
	resultProcVar = syscall.NewCallback(resultPopupWndProc)
}

// ────────────────────────────────────────────────────────────
// QR Popup Window
// ────────────────────────────────────────────────────────────

func showQRPopup(pngBytes []byte, charCount int, wasTruncated bool) {
	// Register class (once)
	registerWindowClass(qrPopupClass, qrProcVar)

	title := fmt.Sprintf("共 %d 字符", charCount)
	if wasTruncated {
		title = fmt.Sprintf("已截断，共 %d 字符", charCount)
	}

	// Decode PNG to get dimensions
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		showError("二维码错误", "无法解码二维码图像")
		return
	}
	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()

	// Create window at 300x300 with resizable border
	hwnd, err := createWindow(
		qrPopupClass, title,
		0x00CF0000|0x00040000|0x00080000, // WS_OVERLAPPEDWINDOW style
		100, 100, 300, 300, // initial position and size
		0,
	)
	if err != nil {
		showError("窗口错误", fmt.Sprintf("无法创建窗口: %v", err))
		return
	}

	// Store per-window data
	data := &qrPopupData{
		pngBytes:   pngBytes,
		charCount:  charCount,
		truncated:  wasTruncated,
		origWidth:  imgW,
		origHeight: imgH,
	}
	// Allocate memory for the pointer and set it as GWLP_USERDATA
	setWindowLongPtr(hwnd, GWLP_USERDATA, uintptr(unsafe.Pointer(data)))

	showWindow(hwnd, 1) // SW_SHOWNORMAL
	updateWindow(hwnd)
}

func qrPopupWndProc(hwnd HANDLE, msg UINT, wParam WPARAM, lParam LPARAM) LRESULT {
	dataPtr := getWindowLongPtr(hwnd, GWLP_USERDATA)
	data := (*qrPopupData)(unsafe.Pointer(dataPtr))

	switch msg {
	case WM_PAINT:
		if data != nil {
			paintQRPopup(hwnd, data)
		}
		return 0

	case WM_SIZE:
		// Force repaint on resize
		invalidateRect(hwnd, nil, true)
		return 0

	case WM_GETMINMAXINFO:
		// Set minimum size 150x150
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize = struct{ X, Y int32 }{150, 150}
		}
		return 0

	case WM_ERASEBKGND:
		// Prevent flicker — we paint everything in WM_PAINT
		return 1

	case WM_RBUTTONUP:
		if data != nil {
			copyQRToClipboard(data.pngBytes)
		}
		return 0

	case WM_CLOSE:
		// Free bitmap and data
		if data != nil && data.hBitmap != 0 {
			deleteBitmap(data.hBitmap)
		}
		destroyWindow(hwnd)
		return 0

	case WM_DESTROY:
		// Clean up allocated data (allocated with new in showQRPopup)
		uiDataMu.Lock()
		delete(uiData, hwnd)
		uiDataMu.Unlock()
		return 0
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	defProc := user32.NewProc("DefWindowProcW")
	ret, _, _ := defProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return LRESULT(ret)
}

func paintQRPopup(hwnd HANDLE, data *qrPopupData) {
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")
	user32 := windows.NewLazySystemDLL("user32.dll")

	// Get client area size
	var rect RECT
	user32.NewProc("GetClientRect").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	cw := int(rect.Right - rect.Left)
	ch := int(rect.Bottom - rect.Top)

	// Decode PNG
	img, err := png.Decode(bytes.NewReader(data.pngBytes))
	if err != nil {
		return
	}

	// Calculate square area that fits in the window
	side := cw
	if ch < side {
		side = ch
	}
	if side < 50 {
		side = 50
	}
	offsetX := (cw - side) / 2
	offsetY := (ch - side) / 2

	// Get DC
	beginPaint := user32.NewProc("BeginPaint")
	var ps PAINTSTRUCT
	hdc, _, _ := beginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer user32.NewProc("EndPaint").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))

	// Fill background white
	fillRect := user32.NewProc("FillRect")
	whiteBrush, _, _ := gdi32.NewProc("GetStockObject").Call(0) // WHITE_BRUSH = 0
	fillRect.Call(hdc, uintptr(unsafe.Pointer(&rect)), whiteBrush)

	// Create compatible DC and bitmap for the image
	createCompatibleDC := gdi32.NewProc("CreateCompatibleDC")
	memDC, _, _ := createCompatibleDC.Call(hdc)
	if memDC == 0 {
		return
	}
	defer gdi32.NewProc("DeleteDC").Call(memDC)

	// Convert image to raw BGRA bytes for CreateDIBitmap
	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()

	bgra := make([]byte, imgW*imgH*4)
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			offset := (y*imgW + x) * 4
			bgra[offset] = byte(b >> 8)   // B
			bgra[offset+1] = byte(g >> 8) // G
			bgra[offset+2] = byte(r >> 8) // R
			bgra[offset+3] = byte(a >> 8) // A
		}
	}

	// Create BITMAPINFO header
	biSize := uint32(unsafe.Sizeof(BITMAPINFOHEADER{}))
	bi := BITMAPINFO{
		Header: BITMAPINFOHEADER{
			Size:        biSize,
			Width:       int32(imgW),
			Height:      -int32(imgH), // negative = top-down
			Planes:      1,
			BitCount:    32,
			Compression: 0, // BI_RGB
			SizeImage:   0,
			XPelsPerMeter: 0,
			YPelsPerMeter: 0,
			ClrUsed:     0,
			ClrImportant: 0,
		},
	}

	createDIBitmap := gdi32.NewProc("CreateDIBitmap")
	hBmp, _, _ := createDIBitmap.Call(
		hdc,
		uintptr(unsafe.Pointer(&bi.Header)),
		4, // CBM_INIT
		uintptr(unsafe.Pointer(&bgra[0])),
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
	)
	if hBmp == 0 {
		return
	}
	defer gdi32.NewProc("DeleteObject").Call(hBmp)

	// Select bitmap into mem DC
	selectObject := gdi32.NewProc("SelectObject")
	oldBmp, _, _ := selectObject.Call(memDC, hBmp)
	defer selectObject.Call(memDC, oldBmp)

	// StretchBlt from mem DC to window DC, keeping square proportions
	stretchBlt := gdi32.NewProc("StretchBlt")
	stretchBlt.Call(
		hdc,
		uintptr(offsetX), uintptr(offsetY), uintptr(side), uintptr(side), // dest
		memDC,
		0, 0, uintptr(imgW), uintptr(imgH), // source
		0x00CC0020, // SRCCOPY
	)
}

func copyQRToClipboard(pngBytes []byte) {
	// Decode PNG to get a HBITMAP
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return
	}

	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()

	bgra := make([]byte, imgW*imgH*4)
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			offset := (y*imgW + x) * 4
			bgra[offset] = byte(b >> 8)
			bgra[offset+1] = byte(g >> 8)
			bgra[offset+2] = byte(r >> 8)
			bgra[offset+3] = 255
		}
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")

	// Get screen DC
	screenDC, _, _ := user32.NewProc("GetDC").Call(0)
	if screenDC == 0 {
		return
	}
	defer user32.NewProc("ReleaseDC").Call(0, screenDC)

	bi := BITMAPINFO{
		Header: BITMAPINFOHEADER{
			Size:        uint32(unsafe.Sizeof(BITMAPINFOHEADER{})),
			Width:       int32(imgW),
			Height:      -int32(imgH),
			Planes:      1,
			BitCount:    32,
			Compression: 0,
		},
	}

	hBmp, _, _ := gdi32.NewProc("CreateDIBitmap").Call(
		screenDC,
		uintptr(unsafe.Pointer(&bi.Header)),
		4,
		uintptr(unsafe.Pointer(&bgra[0])),
		uintptr(unsafe.Pointer(&bi)),
		0,
	)
	if hBmp == 0 {
		return
	}
	defer gdi32.NewProc("DeleteObject").Call(hBmp)

	user32.NewProc("OpenClipboard").Call(uintptr(mainHWND))
	user32.NewProc("EmptyClipboard").Call()
	user32.NewProc("SetClipboardData").Call(2, hBmp) // CF_BITMAP = 2
	user32.NewProc("CloseClipboard").Call()
}

// ────────────────────────────────────────────────────────────
// Scan Preview Window
// ────────────────────────────────────────────────────────────

type scanPreviewData struct {
	detected chan string // sent when QR is found
	done     chan struct{}
}

func showScanPreview(outHwnd *HANDLE) {
	registerWindowClass(scanPreviewClass, scanProcVar)

	hwnd, err := createWindow(
		scanPreviewClass, "扫描二维码",
		0x00CF0000|0x00040000|0x00080000,
		200, 200, 480, 400,
		0,
	)
	if err != nil {
		showError("窗口错误", fmt.Sprintf("无法创建扫描窗口: %v", err))
		return
	}

	data := &scanPreviewData{
		detected: make(chan string, 1),
		done:     make(chan struct{}),
	}
	setWindowLongPtr(hwnd, GWLP_USERDATA, uintptr(unsafe.Pointer(data)))
	uiDataMu.Lock()
	uiData[hwnd] = data
	uiDataMu.Unlock()

	*outHwnd = hwnd

	showWindow(hwnd, 1)
	updateWindow(hwnd)

	// When the user closes the window, signal camera to stop
	// (the done channel is closed in scanPreviewWndProc WM_CLOSE)
}

func scanPreviewWndProc(hwnd HANDLE, msg UINT, wParam WPARAM, lParam LPARAM) LRESULT {
	switch msg {
	case WM_PAINT:
		paintScanPreview(hwnd)
		return 0
	case WM_CLOSE:
		// Signal camera to stop
		dataPtr := getWindowLongPtr(hwnd, GWLP_USERDATA)
		if dataPtr != 0 {
			data := (*scanPreviewData)(unsafe.Pointer(dataPtr))
			select {
			case <-data.done:
				// Already closed
			default:
				close(data.done)
			}
		}
		// Post to main window to clean up
		user32 := windows.NewLazySystemDLL("user32.dll")
		postMsg := user32.NewProc("PostMessageW")
		postMsg.Call(uintptr(mainHWND), WM_CLOSE_PREVIEW, 0, 0)
		return 0
	case WM_ERASEBKGND:
		return 1
	case WM_DESTROY:
		// Handled — do NOT call DefWindowProcW to avoid PostQuitMessage
		uiDataMu.Lock()
		delete(uiData, hwnd)
		uiDataMu.Unlock()
		return 0
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	defProc := user32.NewProc("DefWindowProcW")
	ret, _, _ := defProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return LRESULT(ret)
}

func paintScanPreview(hwnd HANDLE) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")

	var rect RECT
	user32.NewProc("GetClientRect").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	cw := int(rect.Right - rect.Left)
	ch := int(rect.Bottom - rect.Top)

	var ps PAINTSTRUCT
	hdc, _, _ := user32.NewProc("BeginPaint").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer user32.NewProc("EndPaint").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))

	// Fill black background
	blackBrush, _, _ := gdi32.NewProc("GetStockObject").Call(4) // BLACK_BRUSH = 4
	user32.NewProc("FillRect").Call(hdc, uintptr(unsafe.Pointer(&rect)), blackBrush)

	// Draw green guide frame (centered, 60% of window)
	frameW := int32(float64(cw) * 0.6)
	frameH := int32(float64(ch) * 0.6)
	frameX := (int32(cw) - frameW) / 2
	frameY := (int32(ch) - frameH) / 2

	// Create green pen
	createPen := gdi32.NewProc("CreatePen")
	greenPen, _, _ := createPen.Call(0, 3, 0x0000FF00) // PS_SOLID, width 3, BGR green
	if greenPen != 0 {
		oldPen, _, _ := gdi32.NewProc("SelectObject").Call(hdc, greenPen)
		defer gdi32.NewProc("SelectObject").Call(hdc, oldPen)
		defer gdi32.NewProc("DeleteObject").Call(greenPen)
	}

	// Draw hollow rectangle using brush
	nullBrush, _, _ := gdi32.NewProc("GetStockObject").Call(5) // NULL_BRUSH
	oldBrush, _, _ := gdi32.NewProc("SelectObject").Call(hdc, nullBrush)
	defer gdi32.NewProc("SelectObject").Call(hdc, oldBrush)

	selectObject := gdi32.NewProc("SelectObject")
	selectObject.Call(hdc, greenPen)
	selectObject.Call(hdc, nullBrush)

	gdi32.NewProc("Rectangle").Call(
		hdc,
		uintptr(frameX), uintptr(frameY),
		uintptr(frameX+frameW), uintptr(frameY+frameH),
	)

	// Draw hint text below frame
	setBkMode := gdi32.NewProc("SetBkMode")
	setBkMode.Call(hdc, 1) // TRANSPARENT
	setTextColor := gdi32.NewProc("SetTextColor")
	setTextColor.Call(hdc, 0x0000FF00) // Green text

	hintText, _ := syscall.UTF16PtrFromString("请将二维码对准框内")
	textRect := RECT{
		Left:   0,
		Top:    frameY + frameH + 20,
		Right:  int32(cw),
		Bottom: int32(ch),
	}
	user32.NewProc("DrawTextW").Call(
		hdc,
		uintptr(unsafe.Pointer(hintText)),
		^uintptr(0), // -1 means auto-calculate text length
		uintptr(unsafe.Pointer(&textRect)),
		0x00000001|0x00000020, // DT_CENTER | DT_SINGLELINE
	)
}

// ────────────────────────────────────────────────────────────
// Result Popup Window
// ────────────────────────────────────────────────────────────

type resultPopupData struct {
	resultText string
}

func showScanResult(text string) {
	registerWindowClass(resultPopupClass, resultProcVar)

	title := "扫描结果"
	hwnd, err := createWindow(
		resultPopupClass, title,
		0x00CF0000|0x00040000|0x00080000,
		200, 200, 400, 250,
		0,
	)
	if err != nil {
		showError("窗口错误", fmt.Sprintf("无法创建结果窗口: %v", err))
		return
	}

	data := &resultPopupData{resultText: text}
	setWindowLongPtr(hwnd, GWLP_USERDATA, uintptr(unsafe.Pointer(data)))
	uiDataMu.Lock()
	uiData[hwnd] = data
	uiDataMu.Unlock()

	showWindow(hwnd, 1)
	updateWindow(hwnd)
}

func resultPopupWndProc(hwnd HANDLE, msg UINT, wParam WPARAM, lParam LPARAM) LRESULT {
	dataPtr := getWindowLongPtr(hwnd, GWLP_USERDATA)
	data := (*resultPopupData)(unsafe.Pointer(dataPtr))

	switch msg {
	case WM_PAINT:
		if data != nil {
			paintResultPopup(hwnd, data)
		}
		return 0
	case WM_ERASEBKGND:
		return 1
	case WM_CLOSE:
		destroyWindow(hwnd)
		return 0
	case WM_DESTROY:
		// Handled — do NOT call DefWindowProcW to avoid PostQuitMessage
		uiDataMu.Lock()
		delete(uiData, hwnd)
		uiDataMu.Unlock()
		return 0
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	defProc := user32.NewProc("DefWindowProcW")
	ret, _, _ := defProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return LRESULT(ret)
}

func paintResultPopup(hwnd HANDLE, data *resultPopupData) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")

	var rect RECT
	user32.NewProc("GetClientRect").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))

	var ps PAINTSTRUCT
	hdc, _, _ := user32.NewProc("BeginPaint").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer user32.NewProc("EndPaint").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))

	// White background
	whiteBrush, _, _ := gdi32.NewProc("GetStockObject").Call(0) // WHITE_BRUSH
	user32.NewProc("FillRect").Call(hdc, uintptr(unsafe.Pointer(&rect)), whiteBrush)

	// Draw text
	setBkMode := gdi32.NewProc("SetBkMode")
	setBkMode.Call(hdc, 1) // TRANSPARENT

	// Padding
	textRect := RECT{
		Left:   10,
		Top:    10,
		Right:  rect.Right - 10,
		Bottom: rect.Bottom - 10,
	}

	text, _ := syscall.UTF16PtrFromString(data.resultText)
	// DT_WORDBREAK = 0x10, DT_LEFT = 0x00
	user32.NewProc("DrawTextW").Call(
		hdc,
		uintptr(unsafe.Pointer(text)),
		^uintptr(0), // -1 means auto-calculate text length
		uintptr(unsafe.Pointer(&textRect)),
		0x00000000|0x00000010, // DT_LEFT | DT_WORDBREAK
	)
}

// ────────────────────────────────────────────────────────────
// Win32 helper structs and functions
// ────────────────────────────────────────────────────────────

type RECT struct {
	Left, Top, Right, Bottom int32
}

type PAINTSTRUCT struct {
	HDC         HDC
	FErase      BOOL
	RcPaint     RECT
	FRestore    BOOL
	FIncUpdate  BOOL
	RgbReserved [32]byte
}

type BITMAPINFOHEADER struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type BITMAPINFO struct {
	Header BITMAPINFOHEADER
	Colors [1]uint32 // placeholder
}

type MINMAXINFO struct {
	PtReserved     struct{ X, Y int32 }
	PtMaxSize      struct{ X, Y int32 }
	PtMaxPosition  struct{ X, Y int32 }
	PtMinTrackSize struct{ X, Y int32 }
	PtMaxTrackSize struct{ X, Y int32 }
}

func setWindowLongPtr(hwnd HANDLE, index int32, value uintptr) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	setWindowLong := user32.NewProc("SetWindowLongPtrW")
	setWindowLong.Call(uintptr(hwnd), uintptr(index), value)
}

func getWindowLongPtr(hwnd HANDLE, index int32) uintptr {
	user32 := windows.NewLazySystemDLL("user32.dll")
	getWindowLong := user32.NewProc("GetWindowLongPtrW")
	val, _, _ := getWindowLong.Call(uintptr(hwnd), uintptr(index))
	return val
}

func showWindow(hwnd HANDLE, cmdShow int32) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	show := user32.NewProc("ShowWindow")
	show.Call(uintptr(hwnd), uintptr(cmdShow))
}

func updateWindow(hwnd HANDLE) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	update := user32.NewProc("UpdateWindow")
	update.Call(uintptr(hwnd))
}

func destroyWindow(hwnd HANDLE) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	destroy := user32.NewProc("DestroyWindow")
	destroy.Call(uintptr(hwnd))
}

func closeWindow(hwnd HANDLE) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	closeWin := user32.NewProc("CloseWindow")
	closeWin.Call(uintptr(hwnd))
}

func invalidateRect(hwnd HANDLE, rect *RECT, erase bool) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	inv := user32.NewProc("InvalidateRect")
	var b uintptr
	if erase {
		b = 1
	}
	inv.Call(uintptr(hwnd), uintptr(unsafe.Pointer(rect)), b)
}

func deleteBitmap(hBmp HBITMAP) {
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")
	gdi32.NewProc("DeleteObject").Call(uintptr(hBmp))
}

// ────────────────────────────────────────────────────────────
// Message box convenience
// ────────────────────────────────────────────────────────────

const WM_NULL = 0x0000

// Stub — full definition in main.go
// showError and showAboutDialog are defined in main.go
