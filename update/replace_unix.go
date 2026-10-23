//go:build !windows

package update

import "os"

// replaceFile swaps staged into target atomically. Both live in the same
// directory, so os.Rename is a single rename(2) and never leaves a half written
// binary behind. Renaming over a running executable is allowed on unix; the
// running process keeps the old inode.
func replaceFile(staged, target string) error {
	return os.Rename(staged, target)
}

// cleanupOldBinary is a no-op outside Windows: the rename above leaves no
// leftovers.
func cleanupOldBinary(binaryPath string) error { return nil }
