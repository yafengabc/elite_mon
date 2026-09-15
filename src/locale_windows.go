//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// osLocale is Windows' own answer to "what language is this system in".
//
// The Windows display language comes first: that is the language the OS itself
// shows, which is what someone means by their system language - a machine can
// have English menus and a Chinese region. The user locale is the fallback, and
// if both calls fail, systemLocale() falls back to the POSIX variables.
func osLocale() string {
	const localeNameMaxLength = 85 // LOCALE_NAME_MAX_LENGTH
	buf := make([]uint16, localeNameMaxLength)

	if langID, _, _ := procGetUserDefaultUILanguage.Call(); langID != 0 {
		if n, _, _ := procLCIDToLocaleName.Call(langID,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0); n != 0 {
			return syscall.UTF16ToString(buf)
		}
	}
	if n, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

var (
	localeKernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultUILanguage = localeKernel32.NewProc("GetUserDefaultUILanguage")
	procLCIDToLocaleName         = localeKernel32.NewProc("LCIDToLocaleName")
	procGetUserDefaultLocaleName = localeKernel32.NewProc("GetUserDefaultLocaleName")
)
