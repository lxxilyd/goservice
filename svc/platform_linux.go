//go:build linux

package svc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// newPlatformManager picks the init system present on this machine.
//
// Order matters: a container or a router can have leftover SysV scripts while
// systemd is the real init, and OpenWrt ships SysV-looking scripts that are
// driven by procd, so the specific probes come first.
func newPlatformManager(def Definition) (Manager, error) {
	switch {
	case hasSystemd():
		return newSystemdManager(def), nil
	case isProcd():
		return newProcdManager(def), nil
	default:
		return newSysvManager(def), nil
	}
}

// hasSystemd reports whether systemd is the running init system. Checking
// /run/systemd/system (created by systemd itself) is what systemctl and
// cloudflared use, because /sbin/init may still be a symlink in chroots.
func hasSystemd() bool {
	if !fileExists("/run/systemd/system") {
		return false
	}
	_, err := os.Stat("/sbin/systemctl")
	if err == nil {
		return true
	}
	_, err = os.Stat("/usr/bin/systemctl")
	return err == nil
}

// isProcd reports whether this is an OpenWrt style system (procd + rc.common).
// Vendor CPE firmware frequently ships /etc/rc.common even when procd is
// absent, hence the extra /sbin/procd probe.
func isProcd() bool {
	return fileExists("/etc/rc.common") && (fileExists("/sbin/procd") || fileExists("/etc/openwrt_release"))
}

// findProcessByExe returns the pid of the process whose executable resolves to
// exePath, or 0. Scanning /proc avoids depending on pgrep/pidof, which are
// missing or BusyBox variants on most embedded firmware.
func findProcessByExe(exePath string) int {
	target, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		target = exePath
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}

	self := strconv.Itoa(os.Getpid())
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid := entry.Name()
		if pid == self {
			continue
		}
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		link, err := os.Readlink(filepath.Join("/proc", pid, "exe"))
		if err != nil {
			continue
		}
		// The link is suffixed with " (deleted)" for replaced binaries.
		link = strings.TrimSuffix(link, " (deleted)")
		if link == target || link == exePath {
			if n, err := strconv.Atoi(pid); err == nil {
				return n
			}
		}
	}
	return 0
}
