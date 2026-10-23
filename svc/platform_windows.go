//go:build windows

package svc

func newPlatformManager(def Definition) (Manager, error) {
	return newWindowsManager(def), nil
}
