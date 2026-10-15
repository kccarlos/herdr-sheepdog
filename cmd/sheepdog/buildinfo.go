package main

import "runtime/debug"

func init() {
	if bi, ok := debug.ReadBuildInfo(); ok {
		version, commit, date = stampFromBuildInfo(bi, version, commit, date)
	}
}

// stampFromBuildInfo fills the placeholders left by a build without
// -ldflags (go build, go install, the plugin's fallback build) from the
// module version and the VCS stamps Go embeds. Values set by ldflags win.
func stampFromBuildInfo(bi *debug.BuildInfo, version, commit, date string) (string, string, string) {
	var rev, vtime string
	dirty := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			vtime = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if version == "dev" {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
	}
	if commit == "none" && rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if dirty {
			rev += "-dirty"
		}
		commit = rev
	}
	if date == "unknown" && vtime != "" {
		date = vtime
	}
	return version, commit, date
}
