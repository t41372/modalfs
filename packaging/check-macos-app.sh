#!/bin/sh
# Verify the production artifact, including flags that change native visuals.
set -eu
cd "$(dirname "$0")/.."
app=${MODALFS_BUILD_DIR:-dist}/modalfs.app
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

lipo -info "$app/Contents/MacOS/modalfs"
codesign --verify --strict "$app"
"$app/Contents/Resources/bin/modalfs" version
for arch in $(lipo -archs "$app/Contents/MacOS/modalfs"); do
  lipo -thin "$arch" "$app/Contents/MacOS/modalfs" -output "$work/gui-$arch"
  tags=$(go version -m "$work/gui-$arch" | sed -n 's/^[[:space:]]*build[[:space:]]*-tags=//p')
  case ",$tags," in
    *,private_mac_apis,*) ;;
    *) echo "error: $arch app lacks the transparent WebView build flag" >&2; exit 1 ;;
  esac
  case ",$tags," in
    *,mcp,*) echo "error: $arch production app includes the inspection server" >&2; exit 1 ;;
  esac
done
