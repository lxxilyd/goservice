//go:build !windows

package svc

import "context"

// RunServiceIfNeeded is a no-op outside Windows: the unix service managers
// start the process directly, there is no service dispatcher to register with.
// It always reports false so callers fall through to their normal CLI.
func RunServiceIfNeeded(name string, fn func(ctx context.Context) error) (bool, error) {
	return false, nil
}
