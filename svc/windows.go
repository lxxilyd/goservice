//go:build windows

package svc

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	winsvc "golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

type windowsManager struct {
	def Definition
}

func newWindowsManager(def Definition) Manager { return &windowsManager{def: def} }

func (m *windowsManager) Backend() string        { return "windows" }
func (m *windowsManager) Definition() Definition { return m.def }

func (m *windowsManager) startType() uint32 {
	if m.def.AutoStart {
		return mgr.StartAutomatic
	}
	return mgr.StartManual
}

// createConfig is what CreateService gets. It deliberately leaves Description
// empty: x/sys deletes the service again when the follow-up description update
// fails, which is exactly how a Windows install ends up half registered (the
// EventLog source is already there, the service record is not).
func (m *windowsManager) createConfig() mgr.Config {
	return mgr.Config{
		StartType:        m.startType(),
		DisplayName:      m.def.DisplayName,
		Dependencies:     m.def.Dependencies,
		ServiceStartName: m.def.User,
	}
}

// updateConfig reads the current configuration and applies our fields, because
// UpdateConfig sends every field to the SCM (zero values are not "unchanged").
func (m *windowsManager) updateConfig(service *mgr.Service) error {
	current, err := service.Config()
	if err != nil {
		return fmt.Errorf("svc: reading configuration of %s: %w", m.def.Name, err)
	}

	current.StartType = m.startType()
	current.DisplayName = m.def.DisplayName
	current.Description = m.def.Description
	current.Dependencies = m.def.Dependencies
	if m.def.User != "" {
		current.ServiceStartName = m.def.User
	}
	return service.UpdateConfig(current)
}

func (m *windowsManager) Install(ctx context.Context) error {
	conn, err := mgr.Connect()
	if err != nil {
		return connectError(err)
	}
	defer conn.Disconnect()

	service, openErr := conn.OpenService(m.def.Name)
	if openErr == nil {
		defer service.Close()
		if err := m.updateConfig(service); err != nil {
			return err
		}
	} else {
		service, err = conn.CreateService(m.def.Name, m.def.ExecPath, m.createConfig(), m.def.Args...)
		if err != nil {
			return fmt.Errorf("svc: creating service %s: %w", m.def.Name, err)
		}
		defer service.Close()

		// No event log source means no event log, not a broken install: keep
		// going. (cloudflared issue #1498 fails here and deletes the service.)
		_ = eventlog.InstallAsEventCreate(m.def.Name, eventlog.Error|eventlog.Warning|eventlog.Info)

		// Best effort description, after the service record exists.
		if m.def.Description != "" {
			if cfg, cfgErr := service.Config(); cfgErr == nil {
				cfg.Description = m.def.Description
				_ = service.UpdateConfig(cfg)
			}
		}
	}

	if m.def.Restart {
		_ = configureRecovery(service.Handle, m.def.RestartSec)
	}

	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("svc: starting service %s: %w", m.def.Name, err)
	}
	return nil
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	conn, err := mgr.Connect()
	if err != nil {
		return connectError(err)
	}
	defer conn.Disconnect()

	service, err := conn.OpenService(m.def.Name)
	if err != nil {
		// Half installed leftovers: the EventLog source survives the service
		// record, and leaving it behind makes the next install fail.
		_ = eventlog.Remove(m.def.Name)
		return ErrNotInstalled
	}
	defer service.Close()

	_ = m.stopService(service)

	if err := service.Delete(); err != nil {
		return fmt.Errorf("svc: deleting service %s: %w", m.def.Name, err)
	}
	_ = eventlog.Remove(m.def.Name)
	return nil
}

func (m *windowsManager) Start(ctx context.Context) error {
	service, err := m.open()
	if err != nil {
		return err
	}
	defer service.Close()

	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("svc: starting service %s: %w", m.def.Name, err)
	}
	return nil
}

func (m *windowsManager) Stop(ctx context.Context) error {
	service, err := m.open()
	if err != nil {
		return err
	}
	defer service.Close()
	return m.stopService(service)
}

func (m *windowsManager) Restart(ctx context.Context) error {
	service, err := m.open()
	if err != nil {
		return err
	}
	defer service.Close()

	if err := m.stopService(service); err != nil {
		return err
	}
	return service.Start()
}

func (m *windowsManager) Status(ctx context.Context) (Status, error) {
	service, err := m.open()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return Status{State: StateNotInstalled}, nil
		}
		return Status{}, err
	}
	defer service.Close()

	st, err := service.Query()
	if err != nil {
		return Status{}, fmt.Errorf("svc: querying service %s: %w", m.def.Name, err)
	}

	status := Status{PID: int(st.ProcessId), Detail: serviceStateString(st.State)}
	switch st.State {
	case winsvc.Running, winsvc.StartPending, winsvc.ContinuePending:
		status.State = StateRunning
	case winsvc.Stopped, winsvc.StopPending:
		status.State = StateStopped
	default:
		status.State = StateUnknown
	}

	// Report the real command line: a service whose ImagePath lost its
	// arguments starts and exits immediately, which is by far the most common
	// "service installed but not running" report on Windows Server.
	if cfg, cfgErr := service.Config(); cfgErr == nil && cfg.BinaryPathName != "" {
		status.Detail += ": " + cfg.BinaryPathName
	}
	return status, nil
}

func (m *windowsManager) open() (*mgr.Service, error) {
	conn, err := mgr.Connect()
	if err != nil {
		return nil, connectError(err)
	}
	defer conn.Disconnect()

	service, err := conn.OpenService(m.def.Name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return nil, fmt.Errorf(
				"svc: service %s is marked for deletion, close the Services console (or reboot) and retry: %w",
				m.def.Name, err)
		}
		return nil, ErrNotInstalled
	}
	// The Service outlives the manager handle, which is what we want here.
	return service, nil
}

func (m *windowsManager) stopService(service *mgr.Service) error {
	st, err := service.Query()
	if err != nil {
		return err
	}
	if st.State == winsvc.Stopped {
		return nil
	}
	if _, err := service.Control(winsvc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("svc: stopping service %s: %w", m.def.Name, err)
	}

	timeout := time.Now().Add(30 * time.Second)
	for time.Now().Before(timeout) {
		st, err = service.Query()
		if err == nil && st.State == winsvc.Stopped {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("svc: service %s did not stop within 30s", m.def.Name)
}

func connectError(err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("svc: connecting to the service control manager: %w (run as Administrator)", err)
	}
	return fmt.Errorf("svc: connecting to the service control manager: %w", err)
}

func serviceStateString(state winsvc.State) string {
	switch state {
	case winsvc.Stopped:
		return "stopped"
	case winsvc.StartPending:
		return "start-pending"
	case winsvc.StopPending:
		return "stop-pending"
	case winsvc.Running:
		return "running"
	case winsvc.ContinuePending:
		return "continue-pending"
	case winsvc.PausePending:
		return "pause-pending"
	case winsvc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}

// Windows failure actions, see
// https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_failure_actionsa
const serviceConfigFailureActions = 2

type recoveryAction struct {
	recoveryType uint32
	delay        uint32 // milliseconds
}

type serviceFailureActions struct {
	resetPeriod uint32
	rebootMsg   *uint16
	command     *uint16
	actionCount uint32
	actions     uintptr
}

type serviceFailureActionsFlag struct {
	enableActionsForStopsWithErr int32
}

// configureRecovery makes the SCM restart the service after an unexpected exit,
// the Windows equivalent of systemd's Restart=on-failure.
func configureRecovery(handle windows.Handle, delay time.Duration) error {
	actions := []recoveryAction{{
		recoveryType: 1, // SC_ACTION_RESTART
		delay:        uint32(delay / time.Millisecond),
	}}

	failureActions := serviceFailureActions{
		resetPeriod: uint32((24 * time.Hour) / time.Second),
		actionCount: uint32(len(actions)),
		actions:     uintptr(unsafe.Pointer(&actions[0])),
	}
	if err := windows.ChangeServiceConfig2(
		handle,
		windows.SERVICE_CONFIG_FAILURE_ACTIONS,
		(*byte)(unsafe.Pointer(&failureActions)),
	); err != nil {
		return err
	}

	flag := serviceFailureActionsFlag{enableActionsForStopsWithErr: 1}
	return windows.ChangeServiceConfig2(
		handle,
		serviceConfigFailureActions,
		(*byte)(unsafe.Pointer(&flag)),
	)
}

// RunServiceIfNeeded runs fn under the Service Control Manager when the process
// was started by it, and reports whether it handled the process.
//
// Typical main():
//
//	handled, err := svc.RunServiceIfNeeded("mydaemon", run)
//	if handled {
//	    os.Exit(exitCode(err))
//	}
//	// ... normal CLI path
func RunServiceIfNeeded(name string, fn func(ctx context.Context) error) (bool, error) {
	interactive, err := winsvc.IsAnInteractiveSession()
	if err != nil {
		return false, fmt.Errorf("svc: cannot determine session type: %w", err)
	}
	if interactive {
		return false, nil
	}

	err = winsvc.Run(name, &windowsHandler{fn: fn})
	if err != nil {
		// Started from a console (or over a remote shell) but
		// IsAnInteractiveSession reported a service session, see
		// https://github.com/judwhite/go-svc/issues/6: fall back to the CLI.
		var errno windows.Errno
		if errors.As(err, &errno) && errno == windows.ERROR_FAILED_SERVICE_CONTROLLER_CONNECT {
			return false, nil
		}
		return true, err
	}
	return true, nil
}

type windowsHandler struct {
	fn func(ctx context.Context) error
}

func (h *windowsHandler) Execute(args []string, requests <-chan winsvc.ChangeRequest, status chan<- winsvc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status <- winsvc.Status{State: winsvc.StartPending}

	errCh := make(chan error, 1)
	go func() { errCh <- h.fn(ctx) }()

	status <- winsvc.Status{State: winsvc.Running, Accepts: winsvc.AcceptStop | winsvc.AcceptShutdown}

	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case winsvc.Interrogate:
				status <- request.CurrentStatus
			case winsvc.Stop, winsvc.Shutdown:
				status <- winsvc.Status{State: winsvc.StopPending}
				cancel()
			}
		case err := <-errCh:
			if err != nil {
				return true, 1
			}
			return false, 0
		}
	}
}
