// Package version holds the build-time version string, injected via
// -ldflags at build time (see Makefile). Defaults to "dev" for local builds.
package version

var Version = "dev"
