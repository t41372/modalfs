// See native_darwin.h. Wails creates the window and its delegates; this file
// changes three of their methods at startup (closing the window, quitting,
// clicking the Dock icon) and owns the menu bar icon and the app menu.
// Compiled without ARC, like Wails' own Objective-C.

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include "native_darwin.h"
#include "_cgo_export.h"

static NSStatusItem *statusItem;
static NSMutableDictionary *trayImages; // name -> NSImage
static NSWindow *appWindow;

// The window Wails made: the app's only one that can become main. It's kept
// once found, because a closed (hidden) window can't become main, and would
// otherwise never be found again to show.
static NSWindow *mainWindow(void) {
	if (appWindow != nil) return appWindow;
	for (NSWindow *w in [NSApp windows]) {
		if ([w canBecomeMainWindow]) {
			appWindow = [w retain];
			break;
		}
	}
	return appWindow;
}

// A Dock icon while the window is open; menu bar only while it's closed.
static void updatePolicy(void) {
	NSWindow *w = mainWindow();
	BOOL visible = w != nil && [w isVisible];
	if (statusItem == nil || visible) {
		if ([NSApp activationPolicy] != NSApplicationActivationPolicyRegular) {
			[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		}
	} else if ([NSApp activationPolicy] != NSApplicationActivationPolicyAccessory) {
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	}
}

static void showWindow(void) {
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	[NSApp unhide:nil];
	NSWindow *w = mainWindow();
	if (w != nil) {
		if ([w isMiniaturized]) [w deminiaturize:nil];
		[w makeKeyAndOrderFront:nil];
	}
	[NSApp activateIgnoringOtherApps:YES];
}

// Wails stops the app with [NSApp stop:], which takes effect only when the
// next event arrives. Quitting from a goroutine (as OnBeforeClose makes it)
// would otherwise wait for the mouse to move.
void nativeWake(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSEvent *e = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
		[NSApp postEvent:e atStart:YES];
	});
}

void nativeShowWindow(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ showWindow(); });
}

// ----- the methods replaced on Wails' delegates -----

// The red button and ⌘W close the window; modalfs keeps running in the menu bar.
static BOOL mfWindowShouldClose(id self, SEL _cmd, NSWindow *sender) {
	mainWindow(); // found while it's still visible
	[sender orderOut:nil];
	updatePolicy();
	return NO;
}

// Quit from the Dock, or a logout. A person quitting gets the window's quit
// checklist; logging out, restarting or shutting down quits at once (the
// daemon gets its own signal from launchd and unmounts by itself).
static NSApplicationTerminateReply mfShouldTerminate(id self, SEL _cmd, NSApplication *app) {
	NSAppleEventDescriptor *ev = [[NSAppleEventManager sharedAppleEventManager] currentAppleEvent];
	NSAppleEventDescriptor *reason = [ev attributeDescriptorForKeyword:kAEQuitReason];
	if (reason != nil && [reason enumCodeValue] != 0) {
		goSystemQuit();
		return NSTerminateNow;
	}
	goQuitRequested();
	return NSTerminateCancel;
}

// Clicking the Dock icon, or opening the app again while it runs.
static BOOL mfShouldHandleReopen(id self, SEL _cmd, NSApplication *app, BOOL hasVisibleWindows) {
	showWindow();
	return YES;
}

static void replaceMethod(NSString *className, SEL sel, IMP imp, const char *types) {
	Class c = NSClassFromString(className);
	if (c == nil) return;
	Method m = class_getInstanceMethod(c, sel);
	if (m != NULL) method_setImplementation(m, imp);
	else class_addMethod(c, sel, imp, types);
}

// ----- menus -----

@interface MFTarget : NSObject
@end

@implementation MFTarget
- (void)act:(NSMenuItem *)item {
	NSArray *a = [item representedObject];
	if (![a isKindOfClass:[NSArray class]] || [a count] < 2) return;
	goNativeAction((char *)[a[0] UTF8String], (char *)[a[1] UTF8String]);
}
- (void)quit:(id)sender {
	goQuitRequested();
}
@end

static MFTarget *target;

static NSString *str(NSDictionary *d, NSString *key) {
	id v = d[key];
	return [v isKindOfClass:[NSString class]] ? v : @"";
}

static NSMenuItem *item(NSString *title, SEL action, NSString *key) {
	NSMenuItem *i = [[[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key] autorelease];
	return i;
}

static NSMenuItem *actionItem(NSString *title, NSString *action, NSString *ident, NSString *key) {
	NSMenuItem *i = item(title, @selector(act:), key);
	[i setTarget:target];
	[i setRepresentedObject:@[ action, ident ?: @"" ]];
	return i;
}

static NSDictionary *parse(const char *json) {
	NSData *data = [NSData dataWithBytes:json length:strlen(json)];
	id obj = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
	return [obj isKindOfClass:[NSDictionary class]] ? obj : nil;
}

// The app menu, Edit and Window, in the app's language. Quit asks the window
// to unmount first; Close Window only hides it.
void nativeSetMenus(const char *labelsJSON) {
	NSDictionary *l = parse(labelsJSON);
	if (l == nil) return;
	[l retain];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSMenu *bar = [[[NSMenu alloc] initWithTitle:@""] autorelease];

		NSMenu *app = [[[NSMenu alloc] initWithTitle:@"modalfs"] autorelease];
		[app addItem:item(str(l, @"about"), @selector(orderFrontStandardAboutPanel:), @"")];
		[app addItem:[NSMenuItem separatorItem]];
		[app addItem:actionItem(str(l, @"settings"), @"settings", @"", @",")];
		[app addItem:[NSMenuItem separatorItem]];
		[app addItem:item(str(l, @"hide"), @selector(hide:), @"h")];
		NSMenuItem *others = item(str(l, @"hideOthers"), @selector(hideOtherApplications:), @"h");
		[others setKeyEquivalentModifierMask:NSEventModifierFlagOption | NSEventModifierFlagCommand];
		[app addItem:others];
		[app addItem:item(str(l, @"showAll"), @selector(unhideAllApplications:), @"")];
		[app addItem:[NSMenuItem separatorItem]];
		NSMenuItem *quit = item(str(l, @"quit"), @selector(quit:), @"q");
		[quit setTarget:target];
		[app addItem:quit];
		NSMenuItem *appItem = [[[NSMenuItem alloc] init] autorelease];
		[appItem setSubmenu:app];
		[bar addItem:appItem];

		NSMenu *edit = [[[NSMenu alloc] initWithTitle:str(l, @"edit")] autorelease];
		[edit addItem:item(str(l, @"undo"), @selector(undo:), @"z")];
		NSMenuItem *redo = item(str(l, @"redo"), @selector(redo:), @"z");
		[redo setKeyEquivalentModifierMask:NSEventModifierFlagShift | NSEventModifierFlagCommand];
		[edit addItem:redo];
		[edit addItem:[NSMenuItem separatorItem]];
		[edit addItem:item(str(l, @"cut"), @selector(cut:), @"x")];
		[edit addItem:item(str(l, @"copy"), @selector(copy:), @"c")];
		[edit addItem:item(str(l, @"paste"), @selector(paste:), @"v")];
		[edit addItem:item(str(l, @"selectAll"), @selector(selectAll:), @"a")];
		NSMenuItem *editItem = [[[NSMenuItem alloc] init] autorelease];
		[editItem setSubmenu:edit];
		[bar addItem:editItem];

		NSMenu *window = [[[NSMenu alloc] initWithTitle:str(l, @"window")] autorelease];
		[window addItem:item(str(l, @"minimize"), @selector(performMiniaturize:), @"m")];
		[window addItem:item(str(l, @"zoom"), @selector(performZoom:), @"")];
		[window addItem:item(str(l, @"close"), @selector(performClose:), @"w")];
		NSMenuItem *windowItem = [[[NSMenuItem alloc] init] autorelease];
		[windowItem setSubmenu:window];
		[bar addItem:windowItem];

		[NSApp setMainMenu:bar];
		[NSApp setWindowsMenu:window];
		[l release];
	});
}

// ----- the menu bar icon -----

void nativeSetTrayImage(const char *name, const void *png1x, int len1x, const void *png2x, int len2x) {
	NSString *key = [NSString stringWithUTF8String:name];
	NSBitmapImageRep *r1 = [NSBitmapImageRep imageRepWithData:[NSData dataWithBytes:png1x length:len1x]];
	NSBitmapImageRep *r2 = [NSBitmapImageRep imageRepWithData:[NSData dataWithBytes:png2x length:len2x]];
	if (r1 == nil || r2 == nil) return;
	NSImage *img = [[NSImage alloc] initWithSize:NSMakeSize(18, 18)];
	[r1 setSize:NSMakeSize(18, 18)];
	[r2 setSize:NSMakeSize(18, 18)];
	[img addRepresentation:r1];
	[img addRepresentation:r2];
	[img setTemplate:YES];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (trayImages == nil) trayImages = [[NSMutableDictionary alloc] init];
		trayImages[key] = img;
		[img release];
	});
}

// A volume's line: its name, then what's going on in a quieter color.
static NSAttributedString *volumeTitle(NSString *name, NSString *detail) {
	NSFont *font = [NSFont menuFontOfSize:0];
	NSMutableAttributedString *s = [[[NSMutableAttributedString alloc] initWithString:name attributes:@{NSFontAttributeName : font}] autorelease];
	if ([detail length] > 0) {
		NSDictionary *quiet = @{NSFontAttributeName : [NSFont menuFontOfSize:[font pointSize] - 1], NSForegroundColorAttributeName : [NSColor secondaryLabelColor]};
		[s appendAttributedString:[[[NSAttributedString alloc] initWithString:[@"   " stringByAppendingString:detail] attributes:quiet] autorelease]];
	}
	return s;
}

void nativeSetTray(const char *modelJSON) {
	NSDictionary *m = parse(modelJSON);
	if (m == nil) return;
	[m retain];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (statusItem == nil) {
			statusItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength] retain];
		}
		NSImage *img = trayImages[str(m, @"icon")] ?: trayImages[@"menubar"];
		[[statusItem button] setImage:img];
		[[statusItem button] setToolTip:str(m, @"summary")];

		NSMenu *menu = [[[NSMenu alloc] initWithTitle:@"modalfs"] autorelease];
		[menu setAutoenablesItems:NO];
		NSMenuItem *summary = item(str(m, @"summary"), nil, @"");
		[summary setEnabled:NO];
		[menu addItem:summary];

		NSArray *vols = m[@"volumes"];
		if ([vols isKindOfClass:[NSArray class]] && [vols count] > 0) {
			[menu addItem:[NSMenuItem separatorItem]];
			for (NSDictionary *v in vols) {
				if (![v isKindOfClass:[NSDictionary class]]) continue;
				NSMenuItem *i = actionItem(str(v, @"name"), str(v, @"action"), str(v, @"id"), @"");
				[i setAttributedTitle:volumeTitle(str(v, @"name"), str(v, @"detail"))];
				NSImage *sym = [NSImage imageWithSystemSymbolName:str(v, @"symbol") accessibilityDescription:str(v, @"detail")];
				if (sym != nil) [i setImage:sym];
				if ([str(v, @"action") length] == 0) [i setEnabled:NO];
				[menu addItem:i];
			}
		}

		NSString *downloads = str(m, @"downloads");
		if ([downloads length] > 0) {
			[menu addItem:[NSMenuItem separatorItem]];
			NSMenuItem *d = actionItem(downloads, @"downloads", @"", @"");
			NSImage *sym = [NSImage imageWithSystemSymbolName:@"icloud.and.arrow.down" accessibilityDescription:downloads];
			if (sym != nil) [d setImage:sym];
			[menu addItem:d];
		}

		[menu addItem:[NSMenuItem separatorItem]];
		[menu addItem:actionItem(str(m, @"open"), @"open", @"", @"")];
		[menu addItem:actionItem(str(m, @"settings"), @"settings", @"", @",")];
		[menu addItem:[NSMenuItem separatorItem]];
		NSMenuItem *quit = item(str(m, @"quit"), @selector(quit:), @"q");
		[quit setTarget:target];
		[menu addItem:quit];

		[statusItem setMenu:menu];
		updatePolicy();
		[m release];
	});
}

char *nativePreferredLanguage(void) {
	NSString *l = [[NSLocale preferredLanguages] firstObject] ?: @"en";
	return strdup([l UTF8String]);
}

void nativeInit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (target == nil) target = [[MFTarget alloc] init];
		replaceMethod(@"WindowDelegate", @selector(windowShouldClose:), (IMP)mfWindowShouldClose, "c@:@");
		replaceMethod(@"AppDelegate", @selector(applicationShouldTerminate:), (IMP)mfShouldTerminate, "Q@:@");
		replaceMethod(@"AppDelegate", @selector(applicationShouldHandleReopen:hasVisibleWindows:), (IMP)mfShouldHandleReopen, "c@:@c");
	});
}
