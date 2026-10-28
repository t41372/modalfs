package main

// The Objective-C is in native_darwin.m. A file with //export may only
// declare C in its preamble, hence the header.

/*
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	_ "embed"
	"unsafe"
)

//go:embed tray/menubar.png
var trayIdle []byte

//go:embed tray/menubar@2x.png
var trayIdle2x []byte

//go:embed tray/menubar-up.png
var trayUp []byte

//go:embed tray/menubar-up@2x.png
var trayUp2x []byte

//go:embed tray/menubar-attn.png
var trayAttn []byte

//go:embed tray/menubar-attn@2x.png
var trayAttn2x []byte

// The menu bar icon (and the app menu with it) is macOS-only for now.
const hasTray = true

func nativeInit() {
	C.nativeInit()
	setTrayImage("menubar", trayIdle, trayIdle2x)
	setTrayImage("menubar-up", trayUp, trayUp2x)
	setTrayImage("menubar-attn", trayAttn, trayAttn2x)
}

func setTrayImage(name string, png, png2x []byte) {
	n := C.CString(name)
	defer C.free(unsafe.Pointer(n))
	C.nativeSetTrayImage(n, unsafe.Pointer(&png[0]), C.int(len(png)), unsafe.Pointer(&png2x[0]), C.int(len(png2x)))
}

func nativeSetMenus(labels []byte) {
	s := C.CString(string(labels))
	defer C.free(unsafe.Pointer(s))
	C.nativeSetMenus(s)
}

func nativeSetTray(model []byte) {
	s := C.CString(string(model))
	defer C.free(unsafe.Pointer(s))
	C.nativeSetTray(s)
}

func nativeShowWindow() { C.nativeShowWindow() }

func nativeHideWindow() { C.nativeHideWindow() }

func nativeIsSystemQuit() bool { return C.nativeIsSystemQuit() != 0 }

func nativePreferredLanguage() string {
	s := C.nativePreferredLanguage()
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s)
}

//export goNativeAction
func goNativeAction(action, id *C.char) {
	go theApp.nativeAction(C.GoString(action), C.GoString(id))
}

//export goQuitRequested
func goQuitRequested() { go theApp.requestQuit() }
