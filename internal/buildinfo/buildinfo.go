// Package buildinfo reports the running binary's version from Go's build
// information instead of a constant that goes stale on every release.
//
// A binary installed with
//
//	go install github.com/hyukvoid/idemcheck/cmd/idemcheck@vX.Y.Z
//
// carries "vX.Y.Z" as its main module version, stamped by the go tool.
// Local builds (go build, go test) report "(devel)". IdemCheck surfaces
// both without ever hardcoding a version string.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version returns this binary's module version without the leading "v"
// ("X.Y.Z"), or "devel" when the binary was built from a local checkout
// rather than installed at a tagged version.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// Build info can be stripped from the binary; never fail the CLI.
		return "devel"
	}
	return fromModuleVersion(info.Main.Version)
}

// Display renders a version for humans: "vX.Y.Z" for release binaries,
// "devel" for local development builds.
func Display() string {
	return toDisplay(Version())
}

// fromModuleVersion normalizes runtime/debug's main-module version.
// Untagged builds report "(devel)" (or an empty string on old toolchains).
func fromModuleVersion(v string) string {
	switch v {
	case "", "(devel)":
		return "devel"
	default:
		return strings.TrimPrefix(v, "v")
	}
}

// toDisplay adds the conventional "v" prefix back, except for "devel",
// which reads better unprefixed ("IdemCheck devel", not "IdemCheck vdevel").
func toDisplay(v string) string {
	switch {
	case v == "" || v == "devel":
		return "devel"
	case strings.HasPrefix(v, "v"):
		return v
	default:
		return "v" + v
	}
}
