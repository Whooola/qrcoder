#include <windows.h>

// goKeyboardEvent is defined in hotkey.go (exported via cgo).
extern void goKeyboardEvent(DWORD vkCode, DWORD scanCode, DWORD flags,
                             DWORD time, ULONG_PTR dwExtraInfo);

// LowLevelKeyboardProc is the WH_KEYBOARD_LL callback. Windows calls
// it for every keyboard event system-wide. We forward each event to
// the Go side for double-Ctrl detection.
LRESULT CALLBACK LowLevelKeyboardProc(int nCode, WPARAM wParam, LPARAM lParam) {
    if (nCode == HC_ACTION) {
        KBDLLHOOKSTRUCT *kb = (KBDLLHOOKSTRUCT *)lParam;
        goKeyboardEvent(kb->vkCode, kb->scanCode, kb->flags,
                        kb->time, kb->dwExtraInfo);
    }
    return CallNextHookEx(NULL, nCode, wParam, lParam);
}
