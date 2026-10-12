package main

// Wails v2 uses UTType (macOS 11+) for file dialogs but doesn't link the
// framework that defines it, so the final link fails without the first flag.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework UniformTypeIdentifiers -framework Cocoa
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

static void styleWindows(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *w in [NSApp windows]) {
			if ([w contentView]) useMaterial([w contentView]);
		}
	});
}

// "light", "dark", or anything else to follow the system.
static void setAppearance(const char *name) {
	NSString *n = [NSString stringWithUTF8String:name];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSAppearance *a = nil;
		if ([n isEqualToString:@"dark"]) a = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		else if ([n isEqualToString:@"light"]) a = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
		[NSApp setAppearance:a];
		for (NSWindow *w in [NSApp windows]) [w setAppearance:a];
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
