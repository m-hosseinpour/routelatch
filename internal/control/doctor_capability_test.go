package control

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func doctorCheck(t *testing.T, data DoctorData, name string) Check {
	t.Helper()
	for _, check := range data.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor has no %s check: %+v", name, data.Checks)
	return Check{}
}

func TestDoctorReportsAppCapabilities(t *testing.T) {
	st, paths := newServiceStore(t)
	cases := []struct {
		name       string
		tray       func() error
		pidfd      func() error
		trayState  string
		pidfdState string
		trayText   string
		pidfdText  string
	}{
		{
			name: "available", tray: func() error { return nil }, pidfd: func() error { return nil },
			trayState: "ok", pidfdState: "ok",
			trayText: "StatusNotifier", pidfdText: "pidfd_open is allowed",
		},
		{
			name: "tray unavailable", tray: func() error { return errors.New("no StatusNotifierWatcher on the session bus") },
			pidfd:     func() error { return nil },
			trayState: "warn", pidfdState: "ok",
			trayText: "no StatusNotifier tray host", pidfdText: "pidfd_open is allowed",
		},
		{
			name: "pidfd ENOSYS", tray: func() error { return nil },
			pidfd:     func() error { return errors.New("function not implemented") },
			trayState: "ok", pidfdState: "warn",
			trayText: "StatusNotifier", pidfdText: "pidfd_open is not available",
		},
		{
			name: "pidfd EPERM", tray: func() error { return nil },
			pidfd:     func() error { return errors.New("operation not permitted") },
			trayState: "ok", pidfdState: "warn",
			trayText: "StatusNotifier", pidfdText: "pidfd_open is not available",
		},
		{
			name: "not probed", trayState: "warn", pidfdState: "warn",
			trayText: "not probed", pidfdText: "not probed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewService(context.Background(), Options{
				Store: st, Paths: paths, LogOutput: io.Discard,
				TrayProbe: tc.tray, PIDFDProbe: tc.pidfd,
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := svc.doctor(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			tray := doctorCheck(t, data, "tray_available")
			if tray.Status != tc.trayState || !strings.Contains(tray.Detail, tc.trayText) {
				t.Fatalf("tray check = %+v", tray)
			}
			pidfd := doctorCheck(t, data, "pidfd_available")
			if pidfd.Status != tc.pidfdState || !strings.Contains(pidfd.Detail, tc.pidfdText) {
				t.Fatalf("pidfd check = %+v", pidfd)
			}
		})
	}
}

// The doctor payload keeps its documented shape: checks carry name, status,
// detail and fix, and the capability findings are warnings, never doctor
// errors.
func TestDoctorCapabilityChecksKeepShape(t *testing.T) {
	st, paths := newServiceStore(t)
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard,
		TrayProbe: func() error { return errors.New("unavailable") }, PIDFDProbe: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tray_available", "pidfd_available"} {
		check := doctorCheck(t, data, name)
		if check.Status != "ok" && check.Status != "warn" && check.Status != "error" {
			t.Fatalf("%s status = %q", name, check.Status)
		}
		if check.Status == "warn" && check.Fix == "" {
			t.Fatalf("%s has no fix for an unavailable capability", name)
		}
	}
}

func TestDoctorCapabilityProbeTimeout(t *testing.T) {
	oldTimeout := capabilityProbeTimeout
	capabilityProbeTimeout = 20 * time.Millisecond
	defer func() { capabilityProbeTimeout = oldTimeout }()
	st, paths := newServiceStore(t)
	release := make(chan struct{})
	defer close(release)
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard,
		TrayProbe: func() error { <-release; return nil }, PIDFDProbe: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tray := doctorCheck(t, data, "tray_available")
	if tray.Status != "warn" || !strings.Contains(tray.Detail, "did not answer") {
		t.Fatalf("tray check = %+v", tray)
	}
}
