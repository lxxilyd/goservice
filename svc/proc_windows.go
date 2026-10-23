//go:build windows

package svc

// pidAlive is unused on Windows: process state comes from the Service Control
// Manager instead of pid files.
func pidAlive(int) bool { return false }
