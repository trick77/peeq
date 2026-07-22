package download

import "syscall"

// freeBytes returns the free space available (to an unprivileged process)
// on the filesystem containing dir, via statfs(2). Bavail (rather than
// Bfree) is used deliberately: it excludes space reserved for the root
// user, matching what an actual write from this process could use.
//
// Bsize's underlying type differs between linux and darwin, but both
// convert cleanly to uint64, so this single implementation compiles and
// behaves correctly on both platforms (verified via
// GOOS=linux/darwin CGO_ENABLED=0 go build) without a per-OS shim.
func freeBytes(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// FreeBytes is the exported entry point to freeBytes for callers outside this
// package, e.g. the TubeArchivist import's free-space preflight.
func FreeBytes(dir string) (uint64, error) { return freeBytes(dir) }
