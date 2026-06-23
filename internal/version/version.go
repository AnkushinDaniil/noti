// Package version holds the single source of truth for the noti binary version.
package version

// Version is the noti release version, reported by `noti version` and the
// broker /health endpoint. It defaults to the in-repo value and is overridden
// at release time via -ldflags "-X .../internal/version.Version=<tag>".
var Version = "2.0.1"
