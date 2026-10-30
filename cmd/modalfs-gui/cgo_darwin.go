package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// Wails puts an NSVisualEffectView behind the web view with the default
// material, a thick grey that hides the desktop. Jezo compared the materials
// over colorful and dark wallpapers and chose full-screen UI, which lets the
// colors through; the UI's own layers are tuned for it.
static void useMaterial(NSView *view) {
	if ([view isKindOfClass:[NSVisualEffectView class]]) {
		[(NSVisualEffectView *)view setMaterial:NSVisualEffectMaterialFullScreenUI];
	}
	for (NSView *sub in [view subviews]) useMaterial(sub);
}

// The menu bar icon lives in a window of its own (NSStatusBarWindow). It must
// keep the menu bar's appearance: forcing ours on it tints the template image
// for the wrong background, so a light choice draws it black on a dark menu bar.
static BOOL isStatusBarWindow(NSWindow *w) {
	return [NSStringFromClass([w class]) containsString:@"StatusBar"];
}

static void styleWindows(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *w in [NSApp windows]) {
			if (isStatusBarWindow(w)) continue;
			if ([w contentView]) useMaterial([w contentView]);
		}
	});
}

// "light", "dark", or anything else to follow the system. Only our own
// windows change; NSApp's appearance stays the system's, or the menu bar icon
// and its menu would follow the app instead of the menu bar.
static void setAppearance(const char *name) {
	NSString *n = [NSString stringWithUTF8String:name];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSAppearance *a = nil;
		if ([n isEqualToString:@"dark"]) a = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		else if ([n isEqualToString:@"light"]) a = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
		for (NSWindow *w in [NSApp windows]) {
			if (!isStatusBarWindow(w)) [w setAppearance:a];
		}
	});
}
*/
import "C"

import "unsafe"

func styleWindow() { C.styleWindows() }

func setAppearance(name string) {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	C.setAppearance(c)
}
