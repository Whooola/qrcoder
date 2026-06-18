package main

import (
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────
// UI Automation — direct text selection capture
//
// Instead of simulating Ctrl+C (which is unreliable due to UIPI,
// timing, and input injection issues), we use the Windows UI
// Automation COM API to directly read the selected text from
// the focused element in any application.
// ────────────────────────────────────────────────────────────

var (
	// CLSID_CUIAutomation: {FF48DBA4-60EF-4201-AA87-54103EEF594E}
	CLSID_CUIAutomation = ole.NewGUID("{FF48DBA4-60EF-4201-AA87-54103EEF594E}")

	// IID_IUIAutomation: {30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}
	IID_IUIAutomation = ole.NewGUID("{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}")

	// IID_IUIAutomationTextPattern: {32EBA289-3583-42C9-9C59-3B6D9A1E9B6A}
	IID_IUIAutomationTextPattern = ole.NewGUID("{32EBA289-3583-42C9-9C59-3B6D9A1E9B6A}")

	// UIA control pattern IDs
	UIA_TextPatternId int32 = 10014
)

// ────────────────────────────────────────────────────────────
// UIA vtable helper functions
//
// All COM interface pointers use unsafe.Pointer because go-ole
// v1.2.6 QueryInterface returns *ole.IDispatch which is not
// directly assignable to *ole.IUnknown.
// ────────────────────────────────────────────────────────────

// getUIAVTableEntry gets a vtable function pointer at the given index.
func getUIAVTableEntry(unk unsafe.Pointer, index uintptr) uintptr {
	vtable := *(**uintptr)(unk)
	return *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(vtable)) + index*unsafe.Sizeof(uintptr(0))))
}

// callGetFocusedElement: IUIAutomation::GetFocusedElement (vtable[8])
// HRESULT GetFocusedElement([out] IUIAutomationElement **element);
func callGetFocusedElement(uia unsafe.Pointer) *ole.IUnknown {
	fn := getUIAVTableEntry(uia, 8)
	this := uintptr(uia)
	var element *ole.IUnknown
	syscall.SyscallN(fn, this, uintptr(unsafe.Pointer(&element)))
	return element
}

// callGetCurrentPatternAs: IUIAutomationElement::GetCurrentPatternAs (vtable[14])
// HRESULT GetCurrentPatternAs(PATTERNID patternId, REFIID riid, void **patternObject);
func callGetCurrentPatternAs(element unsafe.Pointer, patternId int32, riid *ole.GUID) *ole.IUnknown {
	fn := getUIAVTableEntry(element, 14)
	this := uintptr(element)
	var pattern *ole.IUnknown
	syscall.SyscallN(
		fn,
		this,
		uintptr(patternId),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(&pattern)),
	)
	return pattern
}

// callTextGetSelection: IUIAutomationTextPattern::GetSelection (vtable[5])
// HRESULT GetSelection([out] IUIAutomationTextRangeArray **ranges);
func callTextGetSelection(textPattern unsafe.Pointer) *ole.IUnknown {
	fn := getUIAVTableEntry(textPattern, 5)
	this := uintptr(textPattern)
	var ranges *ole.IUnknown
	syscall.SyscallN(fn, this, uintptr(unsafe.Pointer(&ranges)))
	return ranges
}

// callRangeArrayGetLength: IUIAutomationTextRangeArray::get_Length (vtable[3])
// HRESULT get_Length([out] int *length);
func callRangeArrayGetLength(ranges unsafe.Pointer) int32 {
	fn := getUIAVTableEntry(ranges, 3)
	this := uintptr(ranges)
	var length int32
	syscall.SyscallN(fn, this, uintptr(unsafe.Pointer(&length)))
	return length
}

// callRangeArrayGetElement: IUIAutomationTextRangeArray::GetElement (vtable[4])
// HRESULT GetElement(int index, [out] IUIAutomationTextRange **range);
func callRangeArrayGetElement(ranges unsafe.Pointer, index int32) *ole.IUnknown {
	fn := getUIAVTableEntry(ranges, 4)
	this := uintptr(ranges)
	var textRange *ole.IUnknown
	syscall.SyscallN(fn, this, uintptr(index), uintptr(unsafe.Pointer(&textRange)))
	return textRange
}

// callTextRangeGetText: IUIAutomationTextRange::GetText (vtable[12])
// HRESULT GetText(int maxLength, [out] BSTR *text);
func callTextRangeGetText(textRange unsafe.Pointer, maxLength int32) string {
	fn := getUIAVTableEntry(textRange, 12)
	this := uintptr(textRange)
	var bstr *uint16
	syscall.SyscallN(fn, this, uintptr(maxLength), uintptr(unsafe.Pointer(&bstr)))
	if bstr == nil {
		return ""
	}
	text := bstrToString(bstr)
	sysFreeString(bstr)
	return text
}

// bstrToString converts a BSTR (length-prefixed wide string) to a Go string.
func bstrToString(bstr *uint16) string {
	if bstr == nil {
		return ""
	}
	// BSTR length is stored in the 4 bytes before the string pointer
	lenPtr := (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(bstr)) - 4))
	byteLen := *lenPtr
	if byteLen == 0 {
		return ""
	}
	charLen := byteLen / 2
	return syscall.UTF16ToString(unsafe.Slice(bstr, charLen))
}

// SysFreeString frees a BSTR allocated by COM.
func sysFreeString(bstr *uint16) {
	ole32 := windows.NewLazySystemDLL("oleaut32.dll")
	ole32.NewProc("SysFreeString").Call(uintptr(unsafe.Pointer(bstr)))
}

// ────────────────────────────────────────────────────────────
// Public API
// ────────────────────────────────────────────────────────────

// captureByUIAutomation uses UI Automation to directly read the
// selected text from the currently focused element. Returns empty
// string if no text is selected or UIA is unavailable.
func captureByUIAutomation() string {
	// Create the CUIAutomation COM object
	uiaUnknown, err := ole.CreateInstance(CLSID_CUIAutomation, nil)
	if err != nil {
		return ""
	}
	defer uiaUnknown.Release()

	// QueryInterface for IUIAutomation
	uia, err := uiaUnknown.QueryInterface(IID_IUIAutomation)
	if err != nil {
		return ""
	}
	defer uia.Release()

	// Get the currently focused element (use unsafe.Pointer for go-ole compat)
	focusedElement := callGetFocusedElement(unsafe.Pointer(uia))
	if focusedElement == nil {
		return ""
	}
	defer focusedElement.Release()

	// Get the TextPattern from the focused element
	textPattern := callGetCurrentPatternAs(unsafe.Pointer(focusedElement), UIA_TextPatternId, IID_IUIAutomationTextPattern)
	if textPattern == nil {
		return ""
	}
	defer textPattern.Release()

	// Get the text selection ranges
	ranges := callTextGetSelection(unsafe.Pointer(textPattern))
	if ranges == nil {
		return ""
	}
	defer ranges.Release()

	// Check how many selection ranges exist
	count := callRangeArrayGetLength(unsafe.Pointer(ranges))
	if count <= 0 {
		return ""
	}

	// Get the first selection range
	textRange := callRangeArrayGetElement(unsafe.Pointer(ranges), 0)
	if textRange == nil {
		return ""
	}
	defer textRange.Release()

	// Get the text from the range (-1 = all text in the range)
	text := callTextRangeGetText(unsafe.Pointer(textRange), -1)
	return text
}
