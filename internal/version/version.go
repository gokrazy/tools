package version

import (
	"runtime/debug"
	"strings"
)

func readParts() (revision string, modified, ok bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}
	settings := make(map[string]string)
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	// When built from a local VCS directory, we can use vcs.revision directly.
	if rev, ok := settings["vcs.revision"]; ok {
		return rev, settings["vcs.modified"] == "true", true
	}
	// When built as a Go module (not from a local VCS directory),
	// info.Main.Version is something like v0.0.0-20230107144322-7a5757f46310.
	v := info.Main.Version // for convenience
	if idx := strings.LastIndexByte(v, '-'); idx > -1 {
		return v[idx+1:], false, true
	}
	return "<BUG>", false, false
}

func Read() string {
	revision, modified, ok := readParts()
	if !ok {
		return "<not okay>"
	}
	modifiedSuffix := ""
	if modified {
		modifiedSuffix = " (modified)"
	}

	return "https://github.com/gokrazy/tools/commit/" + revision + modifiedSuffix
}

func ReadBrief() string {
	revision, modified, ok := readParts()
	if !ok {
		return "<not okay>"
	}
	modifiedSuffix := ""
	if modified {
		modifiedSuffix = "+"
	}
	if len(revision) > 6 {
		revision = revision[:6]
	}
	return "g" + revision + modifiedSuffix
}
