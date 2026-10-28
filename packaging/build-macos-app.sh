#!/bin/sh
# Builds dist/modalfs.app (universal arm64 + x86_64) on a Mac.
# Needs Go 1.27.3+ and the Xcode Command Line Tools (xcode-select --install).
set -eu
cd "$(dirname "$0")/.."

[ "$(uname -s)" = Darwin ] || { echo "error: the macOS app has to be built on a Mac" >&2; exit 1; }
# Recent Go toolchains need macOS 13+, so that is the floor. Without these
# flags cgo code targets the build machine's macOS version and the app won't
# launch on older systems. (Set via CGO_* rather than MACOSX_DEPLOYMENT_TARGET
# because only the former is part of Go's build cache key.)
MIN_MACOS=${MIN_MACOS:-13.0}
export CGO_CFLAGS="-O2 -g -mmacosx-version-min=$MIN_MACOS"
export CGO_LDFLAGS="-mmacosx-version-min=$MIN_MACOS"
VERSION=$(sed -n 's/^var Version = "\(.*\)"/\1/p' internal/daemon/daemon.go)
# Native QA can build a separate bundle without replacing a running app.
APP=${MODALFS_BUILD_DIR:-dist}/modalfs.app
rm -rf "$APP" dist/.mac && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/bin" dist/.mac

ARCHS=${ARCHS:-"arm64 amd64"}
gui=""; cli=""
for arch in $ARCHS; do
  echo "building $arch"
  # Wails 3 makes WKWebView transparency opt-in; the material behind it
  # stays invisible without this tag (Wails 2 enabled it by default).
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -tags desktop,production,private_mac_apis -ldflags "-s -w" -o dist/.mac/gui-$arch ./cmd/modalfs-gui
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o dist/.mac/cli-$arch ./cmd/modalfs
  gui="$gui dist/.mac/gui-$arch"; cli="$cli dist/.mac/cli-$arch"
done
lipo -create -output "$APP/Contents/MacOS/modalfs" $gui
lipo -create -output "$APP/Contents/Resources/bin/modalfs" $cli

# icon
ICONSET=dist/.mac/modalfs.iconset; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s packaging/icon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  d=$((s*2)); sips -z $d $d packaging/icon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/modalfs.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>modalfs</string>
  <key>CFBundleDisplayName</key><string>modalfs</string>
  <key>CFBundleIdentifier</key><string>dev.modalfs.app</string>
  <key>CFBundleExecutable</key><string>modalfs</string>
  <key>CFBundleIconFile</key><string>modalfs</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>$MIN_MACOS</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSSupportsAutomaticGraphicsSwitching</key><true/>
</dict>
</plist>
PLIST

# ad-hoc signature, inside out (--deep is deprecated)
codesign --force --sign - "$APP/Contents/Resources/bin/modalfs"
codesign --force --sign - "$APP"
rm -rf dist/.mac
echo
echo "Built $APP ($ARCHS)"
echo "Install:  cp -R $APP /Applications/"
echo "CLI:      sudo ln -sf /Applications/modalfs.app/Contents/Resources/bin/modalfs /usr/local/bin/modalfs"
