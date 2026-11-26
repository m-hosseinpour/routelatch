package tun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

type fakeCore struct {
	calls    *[]string
	starts   int
	stops    int
	startErr error
	stopErr  error
	onStart  func()
}

func (c *fakeCore) Start(context.Context) error {
	c.starts++
	*c.calls = append(*c.calls, "core.start")
	if c.onStart != nil {
		c.onStart()
	}
	return c.startErr
}

func (c *fakeCore) Stop(context.Context) error {
	c.stops++
	*c.calls = append(*c.calls, "core.stop")
	return c.stopErr
}

type fakeFactory struct {
	mu      sync.Mutex
	cores   []*fakeCore
	newErr  error
	socks   []string
	calls   *[]string
	onStart func()
}

func (f *fakeFactory) New(socks string) (Core, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.socks = append(f.socks, socks)
	if f.newErr != nil {
		return nil, f.newErr
	}
	c := &fakeCore{calls: f.calls, onStart: f.onStart}
	f.cores = append(f.cores, c)
	return c, nil
}

type fakeNetwork struct {
	present   bool
	calls     []string
	checkErr  error
	downErr   error
	removeErr error
}

func (n *fakeNetwork) LinkPresent(string) bool { return n.present }
func (n *fakeNetwork) SetDown(context.Context, string) error {
	n.calls = append(n.calls, "down")
	return n.downErr
}
func (n *fakeNetwork) CheckUp(string, []string, []string) error {
	n.calls = append(n.calls, "check")
	return n.checkErr
}
func (n *fakeNetwork) Remove(context.Context, string) error {
	n.calls = append(n.calls, "remove")
	if n.removeErr != nil {
		return n.removeErr
	}
	n.present = false
	return nil
}

var testNow = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

func testService(t *testing.T) (*Service, *fakeFactory, *fakeNetwork, string) {
	t.Helper()
	dir := t.TempDir()
	var calls []string
	netw := &fakeNetwork{}
	factory := &fakeFactory{calls: &calls}
	svc, err := NewService(Options{
		Config:      ServiceConfig{AllowedUID: os.Getuid(), SocksAddress: "127.0.0.1:1080"},
		CoreFactory: factory,
		Network:     netw,
		LeasePath:   filepath.Join(dir, "lease.json"),
		Now:         func() time.Time { return testNow },
		BootID:      func() string { return "boot-test" },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, factory, netw, filepath.Join(dir, "lease.json")
}

func readLeaseFile(t *testing.T, path string) leaseRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	var rec leaseRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("decode lease: %v", err)
	}
	return rec
}

func TestEnableDisableLifecycle(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(factory.cores) != 1 || factory.cores[0].starts != 1 {
		t.Fatalf("cores=%d starts", len(factory.cores))
	}
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != contract.TunOn || !st.Desired || st.Interface != "rl-tun0" {
		t.Fatalf("status=%+v", st)
	}
	if st.DNS != contract.TunDNSDirect || st.UDP != contract.TunUDPBlocked || !st.IPv6 {
		t.Fatalf("status fixed fields=%+v", st)
	}
	if st.Helper != contract.TunHelperInstalled || !st.Since.Equal(testNow) {
		t.Fatalf("helper/since=%+v", st)
	}
	if st.LastError != "" || st.Detail != "" {
		t.Fatalf("clean status carries an error: %+v", st)
	}
	lease := readLeaseFile(t, leasePath)
	if lease.Desired != "on" || lease.BootID != "boot-test" || lease.Interface != "rl-tun0" {
		t.Fatalf("lease=%+v", lease)
	}
	fi, err := os.Stat(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("lease mode=%o", fi.Mode().Perm())
	}

	// Enable is idempotent while on.
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if len(factory.cores) != 1 {
		t.Fatalf("a second core was created: %d", len(factory.cores))
	}

	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if factory.cores[0].stops != 1 {
		t.Fatalf("core stops=%d", factory.cores[0].stops)
	}
	if netw.present {
		t.Fatal("the link was not removed")
	}
	lease = readLeaseFile(t, leasePath)
	if lease.Desired != "off" {
		t.Fatalf("lease after disable=%+v", lease)
	}
	st, _ = svc.Status(context.Background())
	if st.State != contract.TunOff || st.Desired || !st.Since.IsZero() {
		t.Fatalf("status after disable=%+v", st)
	}
	// Down happens before the core stop; removal after it.
	order := strings.Join(netw.calls, ",")
	if !strings.Contains(order, "down,core.stop,remove") {
		t.Fatalf("teardown order=%q", order)
	}
}

func TestEnableRefusesAnExistingInterface(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	netw.present = true
	err := svc.Enable(context.Background())
	var te *contract.TunError
	if !errors.As(err, &te) || te.Kind != contract.StateLocked || te.Reason != contract.TunReasonConflict {
		t.Fatalf("err=%v", err)
	}
	if len(factory.cores) != 0 {
		t.Fatal("a core was created over an existing interface")
	}
	if _, err := os.Stat(leasePath); err == nil {
		t.Fatal("a failed enable wrote a lease")
	}
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunStateError || st.LastError != contract.TunReasonConflict {
		t.Fatalf("status=%+v", st)
	}
}

func TestEnableDoesNotRestartACoreInProcess(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatal("a second core was allowed in one process")
	}
	if len(factory.cores) != 1 {
		t.Fatalf("cores=%d", len(factory.cores))
	}
}

func TestEnableStartFailureCleansUp(t *testing.T) {
	svc, _, netw, leasePath := testService(t)
	// A core that creates the link and then fails to start.
	fa := &errorStartFactory{inner: &fakeFactory{
		calls:   &netw.calls,
		onStart: func() { netw.present = true },
	}}
	svc.factory = fa
	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatal("a failed start was reported as success")
	}
	if fa.core.starts != 1 || fa.core.stops != 1 {
		t.Fatalf("start/stop=%d/%d", fa.core.starts, fa.core.stops)
	}
	if netw.present {
		t.Fatal("the failed start left the link")
	}
	if _, statErr := os.Stat(leasePath); statErr == nil {
		t.Fatal("a failed start wrote a lease")
	}
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunStateError || st.Detail == "" {
		t.Fatalf("status=%+v", st)
	}
}

type errorStartFactory struct {
	inner *fakeFactory
	core  *fakeCore
}

func (f *errorStartFactory) New(socks string) (Core, error) {
	c, err := f.inner.New(socks)
	if err != nil {
		return nil, err
	}
	f.core = c.(*fakeCore)
	f.core.startErr = errors.New("start failed")
	return f.core, nil
}

// startStopFailFactory fails both Start and the cleanup Stop, so a retry could
// stack a second core over one that may still hold resources.
type startStopFailFactory struct {
	inner *fakeFactory
	core  *fakeCore
}

func (f *startStopFailFactory) New(socks string) (Core, error) {
	c, err := f.inner.New(socks)
	if err != nil {
		return nil, err
	}
	f.core = c.(*fakeCore)
	f.core.startErr = errors.New("start failed")
	f.core.stopErr = errors.New("stop failed")
	return f.core, nil
}

// failFirstStartFactory fails only the first core's Start, so a retry can prove
// that a failed first enable does not latch the process.
type failFirstStartFactory struct {
	inner  *fakeFactory
	failed bool
}

func (f *failFirstStartFactory) New(socks string) (Core, error) {
	c, err := f.inner.New(socks)
	if err != nil {
		return nil, err
	}
	fc := c.(*fakeCore)
	if !f.failed {
		f.failed = true
		fc.startErr = errors.New("start failed")
	}
	return fc, nil
}

// assertFailedEnableCanRetry checks that a failed enable tore its core down and
// left no latch, then performs a fresh enable (§10.5).
func assertFailedEnableCanRetry(t *testing.T, svc *Service, factory *fakeFactory, netw *fakeNetwork) {
	t.Helper()
	if len(factory.cores) != 1 || factory.cores[0].starts != 1 {
		t.Fatalf("failed enable cores=%d", len(factory.cores))
	}
	if factory.cores[0].stops != 1 {
		t.Fatalf("failed core stops=%d", factory.cores[0].stops)
	}
	if netw.present {
		t.Fatal("the failed enable left the link")
	}
	if svc.coreCreated {
		t.Fatal("a failed enable latched the core as run")
	}
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunStateError {
		t.Fatalf("status after failure=%+v", st)
	}

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("retry after a failed enable: %v", err)
	}
	if len(factory.cores) != 2 || factory.cores[1].starts != 1 {
		t.Fatalf("retry did not create exactly one fresh core: %d", len(factory.cores))
	}
	st, _ = svc.Status(context.Background())
	if st.State != contract.TunOn || !st.Desired {
		t.Fatalf("status after retry=%+v", st)
	}
}

func TestEnableRetriesAfterAFailedStart(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	svc.factory = &failFirstStartFactory{inner: factory}

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a failed start was reported as success")
	}
	assertFailedEnableCanRetry(t, svc, factory, netw)
}

func TestEnableRefusesRetryWhenAFailedStartCannotStop(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	svc.factory = &startStopFailFactory{inner: factory}

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a failed start was reported as success")
	}
	if !svc.coreCreated {
		t.Fatal("a failed start whose cleanup also failed was not latched")
	}
	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a second core was allowed while the first may still run")
	}
	if len(factory.cores) != 1 {
		t.Fatalf("cores=%d", len(factory.cores))
	}
}

func TestEnableRetriesAfterACheckUpFailure(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	netw.checkErr = errors.New("route missing")

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("unverified coverage was reported as on")
	}
	netw.checkErr = nil
	assertFailedEnableCanRetry(t, svc, factory, netw)
}

func TestEnableRetriesAfterALeaseFailure(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	svc.leasePath = t.TempDir() // a directory cannot be replaced by the lease

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("an enable without a lease was reported as success")
	}
	svc.leasePath = leasePath
	assertFailedEnableCanRetry(t, svc, factory, netw)
}

func TestEnableRefusesRetryAfterAnUnfinishedTeardown(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	netw.removeErr = errors.New("busy")
	svc.factory = &failFirstStartFactory{inner: factory}

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a failed start was reported as success")
	}
	if !svc.ownsLink {
		t.Fatal("the failed teardown was treated as complete")
	}
	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a retry over an unfinished teardown was allowed")
	}
	if len(factory.cores) != 1 {
		t.Fatalf("a second core was created after a failed teardown: %d", len(factory.cores))
	}
}

type stopFailFactory struct {
	inner *fakeFactory
	core  *fakeCore
}

func (f *stopFailFactory) New(socks string) (Core, error) {
	c, err := f.inner.New(socks)
	if err != nil {
		return nil, err
	}
	f.core = c.(*fakeCore)
	f.core.stopErr = errors.New("stop failed")
	return f.core, nil
}

func TestEnableRefusesRetryWhenAStartedCoreCannotStop(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	netw.checkErr = errors.New("route missing")
	svc.factory = &stopFailFactory{inner: factory}

	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("unverified coverage was reported as on")
	}
	if !svc.coreCreated {
		t.Fatal("a core that could not be stopped was not latched")
	}
	if err := svc.Enable(context.Background()); err == nil {
		t.Fatal("a second core was allowed while the first may still run")
	}
	if len(factory.cores) != 1 {
		t.Fatalf("cores=%d", len(factory.cores))
	}
}

func TestEnableVerificationFailureCleansUp(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	netw.checkErr = errors.New("route missing")
	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatal("unverified coverage was reported as on")
	}
	if factory.cores[0].stops != 1 || netw.present {
		t.Fatalf("stop=%d present=%v", factory.cores[0].stops, netw.present)
	}
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunStateError || !strings.Contains(st.Detail, "routes") {
		t.Fatalf("status=%+v", st)
	}
}

func TestEnableLeaseWriteFailureCleansUp(t *testing.T) {
	svc, factory, netw, _ := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	svc.leasePath = t.TempDir() // a directory cannot be replaced by the lease
	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatal("an enable without a lease was reported as success")
	}
	if factory.cores[0].stops != 1 || netw.present {
		t.Fatalf("stop=%d present=%v", factory.cores[0].stops, netw.present)
	}
}

func TestDisableReportsCleanupFailure(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	netw.removeErr = errors.New("busy")
	err := svc.Disable(context.Background())
	if err == nil {
		t.Fatal("a cleanup failure was reported as success")
	}
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunStateError || st.Detail != "TUN cleanup did not finish" {
		t.Fatalf("status=%+v", st)
	}
	if readLeaseFile(t, leasePath).Desired != "off" {
		t.Fatal("the explicit off wish was not recorded")
	}
}

func TestDisableWhileOffIsIdempotent(t *testing.T) {
	svc, _, netw, leasePath := testService(t)
	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(netw.calls) != 0 {
		t.Fatalf("calls=%v", netw.calls)
	}
	if readLeaseFile(t, leasePath).Desired != "off" {
		t.Fatal("disable did not record off")
	}
}

func TestDisableDoesNotRemoveAForeignLink(t *testing.T) {
	svc, _, netw, leasePath := testService(t)
	netw.present = true
	err := svc.Disable(context.Background())
	if err == nil {
		t.Fatal("a foreign link was silently accepted")
	}
	if len(netw.calls) != 0 || !netw.present {
		t.Fatalf("the foreign link was touched: calls=%v present=%v", netw.calls, netw.present)
	}
	if _, statErr := os.Stat(leasePath); statErr == nil {
		t.Fatal("a refused disable wrote a lease")
	}
}

func writeLeaseRecord(t *testing.T, path string, rec leaseRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptRebuildsSameBoot(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	factory.onStart = func() { netw.present = true }
	writeLeaseRecord(t, leasePath, leaseRecord{Desired: "on", BootID: "boot-test", Interface: InterfaceName})
	svc.adopt(context.Background())
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunOn || !st.Desired {
		t.Fatalf("status=%+v", st)
	}
	if len(factory.cores) != 1 {
		t.Fatalf("cores=%d", len(factory.cores))
	}
}

func TestAdoptDiscardsAnotherBoot(t *testing.T) {
	svc, factory, _, leasePath := testService(t)
	writeLeaseRecord(t, leasePath, leaseRecord{Desired: "on", BootID: "boot-other", Interface: InterfaceName})
	svc.adopt(context.Background())
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunOff || st.Desired || len(factory.cores) != 0 {
		t.Fatalf("status=%+v cores=%d", st, len(factory.cores))
	}
	if readLeaseFile(t, leasePath).Desired != "off" {
		t.Fatal("the stale lease was not discarded")
	}
}

func TestAdoptIgnoresCorruptLease(t *testing.T) {
	svc, factory, _, leasePath := testService(t)
	if err := os.WriteFile(leasePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.adopt(context.Background())
	st, _ := svc.Status(context.Background())
	if st.State != contract.TunOff || len(factory.cores) != 0 {
		t.Fatalf("status=%+v cores=%d", st, len(factory.cores))
	}
}

func TestStatusCarriesNoDestination(t *testing.T) {
	svc, _, _, _ := testService(t)
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, leak := range []string{"127.0.0.1", "1080", "canary", "example"} {
		if strings.Contains(text, leak) {
			t.Fatalf("status leaked %q: %s", leak, text)
		}
	}
}

func TestShutdownRemovesCoverageAndKeepsTheLease(t *testing.T) {
	svc, factory, netw, leasePath := testService(t)
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if netw.present || factory.cores[0].stops != 1 {
		t.Fatalf("present=%v stops=%d", netw.present, factory.cores[0].stops)
	}
	if readLeaseFile(t, leasePath).Desired != "on" {
		t.Fatal("a service restart lease was cleared by a signal stop")
	}
}

func TestServiceErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err    error
		code   string
		reason contract.TunReason
	}{
		{&contract.TunError{Kind: contract.Unsupported, Reason: contract.TunReasonPlatformUnsupported}, "unsupported", contract.TunReasonPlatformUnsupported},
		{&contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict}, "state_locked", contract.TunReasonConflict},
		{&contract.TunError{Kind: contract.Unsupported}, "internal", ""},
		{&contract.TunError{Kind: contract.Unsupported, Reason: "canary.invalid"}, "internal", ""},
		{errors.New("plain"), "internal", ""},
	}
	for _, tc := range cases {
		code, reason := serviceErrorCode(tc.err)
		if code != tc.code || reason != tc.reason {
			t.Fatalf("%v -> %q/%q", tc.err, code, reason)
		}
	}
}

func TestClassifyCoreErrorPermission(t *testing.T) {
	err := classifyCoreError(os.ErrPermission)
	var te *contract.TunError
	if !errors.As(err, &te) || te.Kind != contract.Unsupported || te.Reason != contract.TunReasonPlatformUnsupported {
		t.Fatalf("err=%v", err)
	}
}
