//go:build !darwin

package main

func styleWindow() {}

func setAppearance(string) {}

// No menu bar icon or native menus outside macOS yet: closing the window
// quits the window there, and the daemon keeps the mounts.
const hasTray = false

func nativeInit()                     {}
func nativeSetMenus([]byte)           {}
func nativeSetTray([]byte)            {}
func nativeShowWindow()               {}
func nativeHideWindow()               {}
func nativeIsSystemQuit() bool        { return false }
func nativePreferredLanguage() string { return "" }
