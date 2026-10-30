// The macOS menus and window appearance: attributed volume titles, localized
// app menus, and switching between a Dock app and a menu bar-only one.

void nativeInit(void);
void nativeSetMenus(const char *labelsJSON);
void nativeSetTrayImage(const char *name, const void *png1x, int len1x, const void *png2x, int len2x);
void nativeSetTray(const char *modelJSON);
void nativeShowWindow(void);
void nativeHideWindow(void);
int nativeIsSystemQuit(void);
char *nativePreferredLanguage(void);
