package svc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// renderDefinition exercises every field the renderers look at, with values that
// need quoting, XML escaping and env ordering.
func renderDefinition() Definition {
	def := Definition{
		Name:         "my-daemon",
		DisplayName:  "My Daemon",
		Description:  "Example daemon for the renderer tests",
		ExecPath:     "/usr/local/bin/my-daemon",
		Args:         []string{"run", "--config", "/etc/my daemon.yml", "it's"},
		User:         "svcuser",
		Group:        "svcgroup",
		WorkingDir:   "/var/lib/my-daemon",
		LogDir:       "/var/log",
		Env:          map[string]string{"B_TOKEN": "it's secret", "A_MODE": "prod"},
		EnvFile:      "/etc/my-daemon.env",
		Restart:      true,
		RestartSec:   5 * time.Second,
		Dependencies: []string{"network-online.target"},
		Notify:       true,
		AutoStart:    true,
	}
	if err := def.normalize(); err != nil {
		panic(err)
	}
	return def
}

// TestRenderGolden compares every generated artifact against testdata, so a
// change in one backend cannot quietly alter another platform's unit file.
//
// Regenerate after an intentional change:
//
//	UPDATE_GOLDEN=1 go test ./svc
func TestRenderGolden(t *testing.T) {
	def := renderDefinition()

	cases := []struct{ name, content string }{
		{"systemd.service", buildSystemd(systemdUnitFor(def))},
		{"sysv.initd", buildSysv(sysvScriptFor(def, "/var/log"))},
		{"procd.initd", buildProcd(procdScriptFor(def))},
		{"launchd.plist", buildLaunchd(launchdPlistFor(def, def.Name, "/Library/Logs"))},
		{"rcd.rc", buildRcd(rcdScriptFor(def, "/etc/rc.conf"))},
	}

	update := os.Getenv("UPDATE_GOLDEN") != ""
	if update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range cases {
		path := filepath.Join("testdata", c.name+".golden")
		if update {
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with UPDATE_GOLDEN=1 go test ./svc)", path, err)
		}
		if string(want) != c.content {
			t.Errorf("%s drifted:\n--- got ---\n%s\n--- want ---\n%s", path, c.content, want)
		}
	}
}

// TestRenderIsStable renders each artifact repeatedly: Definition.Env is a map,
// so an unsorted iteration would show up here as a unit that changes on every
// install.
func TestRenderIsStable(t *testing.T) {
	def := renderDefinition()

	first := renderAll(def)
	for i := 0; i < 25; i++ {
		next := renderAll(def)
		for name := range first {
			if first[name] != next[name] {
				t.Fatalf("%s differs between renders:\n--- first ---\n%s\n--- then ---\n%s", name, first[name], next[name])
			}
		}
	}
}

func renderAll(def Definition) map[string]string {
	return map[string]string{
		"systemd": buildSystemd(systemdUnitFor(def)),
		"sysv":    buildSysv(sysvScriptFor(def, "/var/log")),
		"procd":   buildProcd(procdScriptFor(def)),
		"launchd": buildLaunchd(launchdPlistFor(def, def.Name, "/Library/Logs")),
		"rcd":     buildRcd(rcdScriptFor(def, "/etc/rc.conf")),
	}
}
