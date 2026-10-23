//go:build linux

package svc

import (
	"net"
	"os"
	"strings"
)

// NotifyReady sends sd_notify(READY=1). It is what Type=notify in the systemd
// unit waits for; call it once the daemon is actually serving.
//
// It is a no-op when the process was not started by systemd (no NOTIFY_SOCKET),
// so it is safe to call unconditionally.
func NotifyReady() error { return notify("READY=1") }

// NotifyStopping tells systemd the daemon started a clean shutdown.
func NotifyStopping() error { return notify("STOPPING=1") }

// NotifyStatus updates the free form status shown by `systemctl status`.
func NotifyStatus(text string) error { return notify("STATUS=" + text) }

// NotifyReloading announces a configuration reload.
func NotifyReloading() error { return notify("RELOADING=1") }

func notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	// Abstract namespace sockets are passed as "@name" and need a NUL prefix.
	addr := socket
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}

	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Write([]byte(state))
	return err
}
