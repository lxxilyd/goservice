//go:build windows

package svc

// backends are the managers this build compiles in; NewWith chooses from here.
var backends = map[string]func(Definition) Manager{
	"windows": newWindowsManager,
}

func newPlatformManager(def Definition) (Manager, error) {
	return newWindowsManager(def), nil
}
