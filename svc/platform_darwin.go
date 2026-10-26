//go:build darwin

package svc

// backends are the managers this build compiles in; NewWith chooses from here.
var backends = map[string]func(Definition) Manager{
	"launchd": newLaunchdManager,
}

func newPlatformManager(def Definition) (Manager, error) {
	return newLaunchdManager(def), nil
}
