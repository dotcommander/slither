package slither

import "runtime/debug"

type BuildInfo struct {
	Module    string `json:"module,omitempty"`
	Version   string `json:"version,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
}

func CurrentBuildInfo() BuildInfo {
	info := BuildInfo{Version: "devel"}
	if build, ok := debug.ReadBuildInfo(); ok {
		info.Module = build.Main.Path
		if build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		info.GoVersion = build.GoVersion
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	return info
}

func (info BuildInfo) Summary() string {
	version := info.Version
	if version == "" {
		version = "devel"
	}
	revision := info.Revision
	if revision == "" {
		revision = "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	modified := "clean"
	if info.Modified {
		modified = "modified"
	}
	if info.GoVersion == "" {
		return version + " (" + revision + ", " + modified + ")"
	}
	return version + " (" + revision + ", " + modified + ", " + info.GoVersion + ")"
}
