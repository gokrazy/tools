package version

import "runtime/debug"

func readParts() (revision string, modified, ok bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}
	settings := make(map[string]string)
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	return settings["vcs.revision"], settings["vcs.modified"] == "true", true
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
