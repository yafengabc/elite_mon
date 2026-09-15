//go:build !windows

package main

// osLocale has no OS answer outside Windows: the POSIX variables that
// systemLocale() reads next are the convention there.
func osLocale() string { return "" }
