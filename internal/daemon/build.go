package daemon

import "runtime/debug"

// Build names the commit this binary was built from: its short hash, with
// "-dirty" when the tree had uncommitted changes. Go records it at build time;
// it's "" when there was nothing to record (go run, or a build outside git).
var Build = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}()

// VersionBuild is the version with the build after it, as people see it: "0.1.0 (abc1234)".
func VersionBuild() string {
	if Build == "" {
		return Version
	}
	return Version + " (" + Build + ")"
}
