//go:build freebsd || openbsd || netbsd

package svc

func newPlatformManager(def Definition) (Manager, error) {
	return newRcdManager(def), nil
}
