//go:build windows

package update

import (
	"os"
)

// replaceFile swaps staged into target on Windows.
//
// The executing image cannot be deleted, but it can be renamed, so the running
// binary is moved aside to <target>.old and the new one takes its place. The
// leftover is removed by CleanupOldBinary on the next start (after the process
// was restarted, the old file is no longer mapped).
func replaceFile(staged, target string) error {
	old := target + ".old"

	// A previous update may have left an .old behind; it must be gone before we
	// can move the current binary into that name.
	_ = os.Remove(old)

	if err := os.Rename(target, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(staged, target)
}

// cleanupOldBinary removes the file left over by replaceFile.
func cleanupOldBinary(binaryPath string) error {
	err := os.Remove(binaryPath + ".old")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
