#!/bin/sh
# Captures the Finder window in the README's hero picture: a volume mounted by
# modalfs, open in Finder. It's a real mount, but of the fake Modal in
# internal/modalfake, from a temporary home, so it touches no account and no
# real volume. Writes docs/screenshots/inputs/finder-datasets-<appearance>.png
# in the Mac's current appearance (light or dark); `bun run screenshots hero`
# then uses it.
#
#   web/scripts/capture-finder.sh
#
# Needs macOS, Go and Swift (Xcode's command line tools). Finder shows the
# window for a few seconds and closes it again.
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
appearance=$(defaults read -g AppleInterfaceStyle 2>/dev/null | tr 'A-Z' 'a-z' || true)
appearance=${appearance:-light}
out="$repo/docs/screenshots/inputs/finder-datasets-$appearance.png"

tmp=$(mktemp -d /tmp/mfs-finder.XXXX) # short: macOS limits socket paths to 104 bytes
mnt="$tmp/datasets"
mkdir -p "$mnt" "$tmp/home" "$tmp/cache" "$tmp/blocks"
fake= run=
cleanup() {
  osascript -e 'tell application "Finder" to close (every window whose name is "datasets")' >/dev/null 2>&1 || true
  # The fake's data is throwaway, so don't wait for modalfs to finish uploading it.
  [ -n "$run" ] && kill "$run" 2>/dev/null || true
  sleep 2
  mount | grep -q " $mnt " && umount -f "$mnt" 2>/dev/null || true
  [ -n "$run" ] && kill -9 "$run" 2>/dev/null || true
  [ -n "$fake" ] && kill "$fake" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

(cd "$repo" && go build -o "$tmp/modalfs" ./cmd/modalfs && go build -o "$tmp/modalfs-fake" ./cmd/modalfs-fake)
cat >"$tmp/winid.swift" <<'EOF'
import CoreGraphics
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as! [[String: Any]]
for w in list where (w[kCGWindowOwnerName as String] as? String) == "Finder" && (w[kCGWindowName as String] as? String) == CommandLine.arguments[1] {
  print(w[kCGWindowNumber as String]!)
  break
}
EOF
swiftc -O -o "$tmp/winid" "$tmp/winid.swift"

"$tmp/modalfs-fake" -dir "$tmp/blocks" >"$tmp/fake.env" 2>/dev/null &
fake=$!
sleep 1
eval "$(cat "$tmp/fake.env")"
MODALFS_HOME="$tmp/home" MODALFS_NO_KEYCHAIN=1 "$tmp/modalfs" run --create --cache-dir "$tmp/cache" datasets "$mnt" >"$tmp/run.log" 2>&1 &
run=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do mount | grep -q " $mnt " && break; sleep 1; done

# A dataset's files, from this process (never from the one serving the mount).
# Sparse, so they cost nothing to "upload" to the fake.
for i in 0 1 2 3; do truncate -s $((1900 + i * 37))M "$mnt/train-0000$i-of-00004.parquet"; done
truncate -s 1300M "$mnt/train-extra-00000-of-00002.parquet"
truncate -s 1290M "$mnt/train-extra-00001-of-00002.parquet"
truncate -s 412M "$mnt/validation.parquet"
truncate -s 380M "$mnt/test.parquet"
printf '# datasets\n\nVision training data, shared by the research team.\n' >"$mnt/README.md"
printf '{"name":"vision-2024","splits":{"train":1281167,"validation":50000,"test":100000}}\n' >"$mnt/dataset_info.json"

osascript >/dev/null <<EOF
tell application "Finder"
  set w to make new Finder window to (POSIX file "$mnt" as alias)
  set current view of w to list view
  set toolbar visible of w to true
  set statusbar visible of w to false
  set sidebar width of w to 0
  set bounds of w to {200, 200, 900, 512}
  activate
end tell
EOF
sleep 2
id=$("$tmp/winid" datasets)
mkdir -p "$(dirname "$out")"
screencapture -o -l "$id" "$out"
echo "$out"
