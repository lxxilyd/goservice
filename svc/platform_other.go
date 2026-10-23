//go:build !linux && !darwin && !windows && !freebsd && !openbsd && !netbsd

package svc

func newPlatformManager(def Definition) (Manager, error) {
	return nil, ErrUnsupported
}
