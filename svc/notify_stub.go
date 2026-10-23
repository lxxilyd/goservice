//go:build !linux

package svc

// On non-Linux platforms sd_notify does not exist; these are no-ops so calling
// code stays portable.

func NotifyReady() error             { return nil }
func NotifyStopping() error          { return nil }
func NotifyStatus(text string) error { return nil }
func NotifyReloading() error         { return nil }
