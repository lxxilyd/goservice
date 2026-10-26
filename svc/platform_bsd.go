//go:build freebsd || openbsd || netbsd

package svc

// backends are the managers this build compiles in; NewWith chooses from here.
var backends = map[string]func(Definition) Manager{
	"rcd": newRcdManager,
}

func newPlatformManager(def Definition) (Manager, error) {
	return newRcdManager(def), nil
}
