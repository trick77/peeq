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
	// Bsize is a filesystem block size reported by the kernel: always positive
	// and a small power of two, so the int64 to uint64 conversion (linux; on
	// darwin Bsize is already unsigned) cannot wrap.
	return stat.Bavail * uint64(stat.Bsize), nil //nolint:gosec // see above
}
