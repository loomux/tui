// Package version reports the client's own version and the
// github.com/Loomux/server release it is pinned to.
//
// The client has its own SemVer line, independent of the server's and the
// web's (see the design in loomux/server, docs/design/tui-client-design.md,
// "Releases and versioning").
package version

import "runtime/debug"

//go:generate go run gen_serverpin.go

// Version is the client's release version, stamped by the release build:
//
//	go build -ldflags "-X github.com/Loomux/tui/internal/version.Version=0.1.0"
//
// Empty when built without that flag; Client then falls back to the
// module version in the build info.
var Version = ""

// Client returns the client's version: Version when the build stamped it,
// otherwise the main module's version from the build info (vX.Y.Z from
// `go install github.com/Loomux/tui/cmd/loomux@vX.Y.Z`, or the
// pseudo-version Go stamps for a build in a git checkout), otherwise "dev".
func Client() string {
	bi, ok := debug.ReadBuildInfo()
	return client(Version, bi, ok)
}

func client(stamped string, bi *debug.BuildInfo, ok bool) string {
	if stamped != "" {
		return stamped
	}
	if ok && bi != nil {
		// "(devel)" is what a build without a module version reports.
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
