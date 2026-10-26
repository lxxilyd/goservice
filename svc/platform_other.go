//go:build !linux && !darwin && !windows && !freebsd && !openbsd && !netbsd

package svc

// backends is empty here: no service manager is known for this platform, so
// NewWith reports ErrUnsupported for every name.
var backends = map[string]func(Definition) Manager{}

func newPlatformManager(def Definition) (Manager, error) {
	return nil, ErrUnsupported
}
