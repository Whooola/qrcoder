package main

import (
	"log"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// UI Automation — direct text selection capture
//
// Uses Windows UI Automation COM API to directly read selected
// text from the focused element — no clipboard, no keystrokes.
//
// IUIAutomation vtable (after IUnknown [0-2]):
//   [3]  CompareElements
//   [4]  CompareRuntimeIds
//   [5]  GetRootElement
//   [6]  ElementFromHandle
//   [7]  ElementFromPoint
//   [8]  GetFocusedElement  ← this is what we need
//   [9]  GetRootElementBuildCache
//   ...
//
// IUIAutomationElement vtable:
//   [14] GetCurrentPatternAs
//
// IUIAutomationTextPattern vtable:
//   [5]  GetSelection
//
// IUIAutomationTextRangeArray vtable:
//   [3]  get_Length
//   [4]  GetElement
//
// IUIAutomationTextRange vtable:
//   [12] GetText
// ────────────────────────────────────────────────────────────

var (
	CLSID_CUIAutomation          = ole.NewGUID("{FF48DBA4-60EF-4201-AA87-54103EEF594E}")
	IID_IUIAutomation            = ole.NewGUID("{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}")
	IID_IUIAutomationTextPattern = ole.NewGUID("{32EBA289-3583-42C9-9C59-3B6D9A1E9B6A}")

	UIA_TextPatternId int32 = 10014
)

// Pre-loaded DLL procedures to avoid per-call lazy loading.
var (
	procSysFreeString *windows.LazyProc
)

func init() {
	ole32 := windows.NewLazySystemDLL("oleaut32.dll")
	procSysFreeString = ole32.NewProc("SysFreeString")
}

// ────────────────────────────────────────────────────────────
// vtable helpers
// ────────────────────────────────────────────────────────────

func getVTableEntry(unk unsafe.Pointer, index uintptr) uintptr {
	vtable := *(**uintptr)(unk)
	return *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + index*unsafe.Sizeof(uintptr(0))))
}

// callGetFocusedElement: IUIAutomation::GetFocusedElement (vtable[8])
func callGetFocusedElement(uia unsafe.Pointer) (*ole.IUnknown, uintptr) {
	fn := getVTableEntry(uia, 8)
	var element *ole.IUnknown
	hr, _, _ := syscall.SyscallN(fn, uintptr(uia), uintptr(unsafe.Pointer(&element)))
	return element, hr
}

// callGetCurrentPatternAs: IUIAutomationElement::GetCurrentPatternAs (vtable[14])
func callGetCurrentPatternAs(element unsafe.Pointer, patternId int32, riid *ole.GUID) (*ole.IUnknown, uintptr) {
	fn := getVTableEntry(element, 14)
	var pattern *ole.IUnknown
	hr, _, _ := syscall.SyscallN(
		fn, uintptr(element),
		uintptr(patternId),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(&pattern)),
	)
	return pattern, hr
}

// callTextGetSelection: IUIAutomationTextPattern::GetSelection (vtable[5])
func callTextGetSelection(textPattern unsafe.Pointer) (*ole.IUnknown, uintptr) {
	fn := getVTableEntry(textPattern, 5)
	var ranges *ole.IUnknown
	hr, _, _ := syscall.SyscallN(fn, uintptr(textPattern), uintptr(unsafe.Pointer(&ranges)))
	return ranges, hr
}

// callRangeArrayGetLength: IUIAutomationTextRangeArray::get_Length (vtable[3])
func callRangeArrayGetLength(ranges unsafe.Pointer) (int32, uintptr) {
	fn := getVTableEntry(ranges, 3)
	var length int32
	hr, _, _ := syscall.SyscallN(fn, uintptr(ranges), uintptr(unsafe.Pointer(&length)))
	return length, hr
}

// callRangeArrayGetElement: IUIAutomationTextRangeArray::GetElement (vtable[4])
func callRangeArrayGetElement(ranges unsafe.Pointer, index int32) (*ole.IUnknown, uintptr) {
	fn := getVTableEntry(ranges, 4)
	var textRange *ole.IUnknown
	hr, _, _ := syscall.SyscallN(fn, uintptr(ranges), uintptr(index), uintptr(unsafe.Pointer(&textRange)))
	return textRange, hr
}

// callTextRangeGetText: IUIAutomationTextRange::GetText (vtable[12])
func callTextRangeGetText(textRange unsafe.Pointer, maxLength int32) (string, uintptr) {
	fn := getVTableEntry(textRange, 12)
	var bstr *uint16
	hr, _, _ := syscall.SyscallN(fn, uintptr(textRange), uintptr(maxLength), uintptr(unsafe.Pointer(&bstr)))
	if bstr == nil {
		return "", hr
	}
	text := bstrToString(bstr)
	procSysFreeString.Call(uintptr(unsafe.Pointer(bstr)))
	return text, hr
}

// bstrToString converts a BSTR (length-prefixed wide string) to a Go string.
func bstrToString(bstr *uint16) string {
	lenPtr := (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(bstr)) - 4))
	byteLen := *lenPtr
	if byteLen == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(bstr, byteLen/2))
}

// ────────────────────────────────────────────────────────────
// Public API
// ────────────────────────────────────────────────────────────

// captureByUIAutomation uses UI Automation to directly read the
// selected text from the currently focused element.
func captureByUIAutomation() string {
	uiaUnknown, err := ole.CreateInstance(CLSID_CUIAutomation, nil)
	if err != nil {
		log.Printf("UIA: CreateInstance failed: %v", err)
		return ""
	}
	defer uiaUnknown.Release()

	uia, err := uiaUnknown.QueryInterface(IID_IUIAutomation)
	if err != nil {
		log.Printf("UIA: QueryInterface IUIAutomation failed: %v", err)
		return ""
	}
	defer uia.Release()

	focusedElement, hr := callGetFocusedElement(unsafe.Pointer(uia))
	if hr != 0 {
		log.Printf("UIA: GetFocusedElement HRESULT=0x%X", hr)
		return ""
	}
	if focusedElement == nil {
		log.Printf("UIA: GetFocusedElement returned nil element")
		return ""
	}
	defer focusedElement.Release()

	textPattern, hr := callGetCurrentPatternAs(unsafe.Pointer(focusedElement), UIA_TextPatternId, IID_IUIAutomationTextPattern)
	if hr != 0 {
		log.Printf("UIA: GetCurrentPatternAs(TextPattern) HRESULT=0x%X", hr)
		return ""
	}
	if textPattern == nil {
		log.Printf("UIA: focused element does not support TextPattern")
		return ""
	}
	defer textPattern.Release()

	ranges, hr := callTextGetSelection(unsafe.Pointer(textPattern))
	if hr != 0 {
		log.Printf("UIA: TextPattern::GetSelection HRESULT=0x%X", hr)
		return ""
	}
	if ranges == nil {
		log.Printf("UIA: TextPattern::GetSelection returned nil")
		return ""
	}
	defer ranges.Release()

	count, hr := callRangeArrayGetLength(unsafe.Pointer(ranges))
	if hr != 0 {
		log.Printf("UIA: RangeArray::get_Length HRESULT=0x%X", hr)
		return ""
	}
	if count <= 0 {
		log.Printf("UIA: no selection ranges (count=%d)", count)
		return ""
	}

	textRange, hr := callRangeArrayGetElement(unsafe.Pointer(ranges), 0)
	if hr != 0 {
		log.Printf("UIA: RangeArray::GetElement(0) HRESULT=0x%X", hr)
		return ""
	}
	if textRange == nil {
		log.Printf("UIA: RangeArray::GetElement(0) returned nil")
		return ""
	}
	defer textRange.Release()

	text, hr := callTextRangeGetText(unsafe.Pointer(textRange), -1)
	if hr != 0 {
		log.Printf("UIA: TextRange::GetText HRESULT=0x%X", hr)
		return ""
	}

	if text != "" {
		log.Printf("UIA: captured %d chars", len([]rune(text)))
	}
	return text
}
