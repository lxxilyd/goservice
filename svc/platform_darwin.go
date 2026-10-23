//go:build darwin

package svc

func newPlatformManager(def Definition) (Manager, error) {
	return newLaunchdManager(def), nil
}
