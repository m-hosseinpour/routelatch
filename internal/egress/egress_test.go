package egress

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseKernelReleaseBoundary(t *testing.T) {
	for _, tc := range []struct {
		release string
		ok      bool
	}{
		{"5.6.20-generic", false},
		{"5.7.0-rc1", true},
		{"6.8.0-44-generic", true},
		{"4.19.0", false},
		{"7.0.0-44-generic", true},
	} {
		major, minor, err := parseKernelRelease(tc.release)
		if err != nil {
			t.Fatalf("%s: %v", tc.release, err)
		}
		ok := major > minBindDeviceMajor || (major == minBindDeviceMajor && minor >= minBindDeviceMinor)
		if ok != tc.ok {
			t.Fatalf("%s: got supported=%v want %v", tc.release, ok, tc.ok)
		}
	}
	if _, _, err := parseKernelRelease("garbage"); err == nil {
		t.Fatal("a malformed kernel release was accepted")
	}
}

func TestDefaultInterfaceV4(t *testing.T) {
	body := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0102A8C0	0003	0	0	100	00000000	0	0	0
wlan0	00000000	0102A8C0	0003	0	0	10	00000000	0	0	0
rl-tun0	00000000	0102A8C0	0003	0	0	0	00000000	0	0	0
`
	path := filepath.Join(t.TempDir(), "route")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := defaultInterfaceV4(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "wlan0" {
		t.Fatalf("interface=%q, want wlan0 (lowest metric, TUN ignored)", got)
	}
}

func TestDefaultInterfaceV6(t *testing.T) {
	body := `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000001 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00000001 rl-tun0
`
	path := filepath.Join(t.TempDir(), "route6")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := defaultInterfaceV6(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "eth0" {
		t.Fatalf("interface=%q, want eth0", got)
	}
}

type countingRawConn struct{ calls int }

func (c *countingRawConn) Control(f func(fd uintptr)) error {
	c.calls++
	f(^uintptr(0))
	return nil
}

func (c *countingRawConn) Read(func(fd uintptr) (done bool)) error  { return nil }
func (c *countingRawConn) Write(func(fd uintptr) (done bool)) error { return nil }

func TestControllerControlLoopbackAndInactive(t *testing.T) {
	c := NewController()
	raw := &countingRawConn{}
	if err := c.Control("tcp", "127.0.0.1:8080", raw); err != nil {
		t.Fatalf("inactive control: %v", err)
	}
	if raw.calls != 0 {
		t.Fatalf("inactive controller touched the descriptor")
	}
	if err := c.Activate("eth0"); err != nil {
		t.Fatal(err)
	}
	raw = &countingRawConn{}
	if err := c.Control("tcp", "[::1]:8080", raw); err != nil {
		t.Fatalf("loopback control: %v", err)
	}
	if raw.calls != 0 {
		t.Fatalf("loopback dial was bound to the physical interface")
	}
	raw = &countingRawConn{}
	if err := c.Control("tcp", "203.0.113.9:443", raw); err == nil {
		t.Fatal("a failed SO_BINDTODEVICE result was reported as a successful bypass")
	}
	if raw.calls != 1 {
		t.Fatalf("non-loopback dial did not attempt the bypass")
	}
	c.Deactivate()
	raw = &countingRawConn{}
	if err := c.Control("tcp", "203.0.113.9:443", raw); err != nil {
		t.Fatalf("after deactivate: %v", err)
	}
	if raw.calls != 0 {
		t.Fatalf("deactivated controller still binds dials")
	}
}

// TestControllerControlBindsRealSocket proves the controller's result reflects
// the kernel call instead of discarding it. It uses an unconnected real socket
// and the loopback device, which exists without changing host networking.
func TestControllerControlBindsRealSocket(t *testing.T) {
	c := NewController()
	if err := c.Activate("lo"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw, err := client.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Control("tcp", "203.0.113.9:443", raw); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("this kernel does not allow unprivileged SO_BINDTODEVICE")
		}
		t.Fatalf("bind to a real device failed: %v", err)
	}
}

type failingRawConn struct{}

func (failingRawConn) Control(func(uintptr)) error           { return errors.New("raw control failed") }
func (failingRawConn) Read(func(uintptr) (done bool)) error  { return nil }
func (failingRawConn) Write(func(uintptr) (done bool)) error { return nil }

func TestControllerControlPropagatesRawControlError(t *testing.T) {
	c := NewController()
	if err := c.Activate("eth0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Control("tcp", "203.0.113.9:443", failingRawConn{}); err == nil {
		t.Fatal("a failing RawConn.Control was reported as success")
	}
}

func TestControllerControlRejectsUnparsableWhileActive(t *testing.T) {
	c := NewController()
	if err := c.Activate("eth0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Control("tcp", "not-an-address", &countingRawConn{}); err == nil {
		t.Fatal("active controller accepted an unparsable address")
	}
}
