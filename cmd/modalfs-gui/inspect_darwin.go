//go:build darwin && mcp

package main

// Native window state for the opt-in Wails inspection build. The production
// app has neither this service method nor the MCP server.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>

static WKWebView *inspectWebview(NSView *view) {
    if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
    for (NSView *child in view.subviews) {
        WKWebView *found = inspectWebview(child);
        if (found != nil) return found;
    }
    return nil;
}

static void inspectMaterials(NSView *view, NSMutableArray *materials) {
    if ([view isKindOfClass:[NSVisualEffectView class]]) {
        NSVisualEffectView *effect = (NSVisualEffectView *)view;
        [materials addObject:@{
            @"material": @(effect.material),
            @"fullScreenUI": @((BOOL)(effect.material == NSVisualEffectMaterialFullScreenUI)),
            @"behindWindow": @((BOOL)(effect.blendingMode == NSVisualEffectBlendingModeBehindWindow))
        }];
    }
    for (NSView *child in view.subviews) inspectMaterials(child, materials);
}

static char *inspectNativeWindow(void) {
    __block char *result = NULL;
    void (^inspect)(void) = ^{
        @autoreleasepool {
            NSMutableArray *windows = [NSMutableArray array];
            for (NSWindow *window in NSApp.windows) {
                WKWebView *webview = inspectWebview(window.contentView);
                if (webview == nil) continue;
                id drawsBackground = [NSNull null];
                @try { drawsBackground = [webview valueForKey:@"drawsBackground"]; }
                @catch (NSException *exception) {}
                NSMutableArray *materials = [NSMutableArray array];
                inspectMaterials(window.contentView, materials);
                NSString *appearance = [window.effectiveAppearance bestMatchFromAppearancesWithNames:
                    @[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
                [windows addObject:@{
                    @"visible": @(window.visible),
                    @"key": @(window.keyWindow),
                    @"opaque": @(window.opaque),
                    @"appearance": appearance ?: @"",
                    @"drawsBackground": drawsBackground ?: [NSNull null],
                    @"materials": materials
                }];
            }
            NSString *systemAppearance = [NSApp.effectiveAppearance bestMatchFromAppearancesWithNames:
                @[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
            NSDictionary *state = @{@"hidden": @(NSApp.hidden), @"windows": windows,
                @"regularPolicy": @((BOOL)(NSApp.activationPolicy == NSApplicationActivationPolicyRegular)),
                @"systemAppearance": systemAppearance ?: @""};
            NSData *data = [NSJSONSerialization dataWithJSONObject:state options:0 error:nil];
            NSString *json = [[[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] autorelease];
            result = strdup(json.UTF8String);
        }
    };
    if (NSThread.isMainThread) inspect();
    else dispatch_sync(dispatch_get_main_queue(), inspect);
    return result;
}

static char *requestNativeReopen(void) {
    __block char *message = NULL;
    void (^request)(void) = ^{
        @autoreleasepool {
            NSAppleEventDescriptor *event = [NSAppleEventDescriptor
                appleEventWithEventClass:kCoreEventClass eventID:kAEReopenApplication
                targetDescriptor:[NSAppleEventDescriptor currentProcessDescriptor]
                returnID:kAutoGenerateReturnID transactionID:kAnyTransactionID];
            NSError *error = nil;
            [event sendEventWithOptions:NSAppleEventSendNoReply timeout:5 error:&error];
            if (error != nil) message = strdup(error.localizedDescription.UTF8String);
        }
    };
    if (NSThread.isMainThread) request();
    else dispatch_sync(dispatch_get_main_queue(), request);
    return message;
}

static void requestNativeQuit(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

func (a *App) NativeAudit() (map[string]any, error) {
	raw := C.inspectNativeWindow()
	defer C.free(unsafe.Pointer(raw))
	var state map[string]any
	if err := json.Unmarshal([]byte(C.GoString(raw)), &state); err != nil {
		return nil, err
	}
	state["quitAcknowledged"] = a.acked.Load()
	state["quitting"] = a.quitting.Load()
	return state, nil
}

// NativeAuditReopen sends a real reopen event to this process only.
func (*App) NativeAuditReopen() error {
	message := C.requestNativeReopen()
	if message == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(message))
	return fmt.Errorf("send native reopen event: %s", C.GoString(message))
}

// NativeAuditQuit enters AppKit termination without setting the quitting flag.
func (*App) NativeAuditQuit() { C.requestNativeQuit() }
