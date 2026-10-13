// The macOS parts Wails v2 doesn't offer: the menu bar icon, a localized app
// menu whose Quit asks the window first, closing the window without quitting,
// and switching between a Dock app and a menu bar-only one.

void nativeInit(void);
void nativeSetMenus(const char *labelsJSON);
void nativeSetTrayImage(const char *name, const void *png1x, int len1x, const void *png2x, int len2x);
void nativeSetTray(const char *modelJSON);
void nativeShowWindow(void);
void nativeWake(void);
char *nativePreferredLanguage(void);
