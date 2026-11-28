package probes

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// stubConn is a net.Conn with no kernel socket, so a probe test can count
// close calls without leaking goroutines or file descriptors.
type stubConn struct {
	data   []byte
	closed atomic.Int32
}

func (c *stubConn) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, nil
}

func (c *stubConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *stubConn) Close() error                     { c.closed.Add(1); return nil }
func (c *stubConn) LocalAddr() net.Addr              { return stubAddr{} }
func (c *stubConn) RemoteAddr() net.Addr             { return stubAddr{} }
func (c *stubConn) SetDeadline(time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr struct{}

func (stubAddr) Network() string { return "tcp" }
func (stubAddr) String() string  { return "stub" }

// blockingConn parks Read until Close, so a test can cancel a first-byte wait
// and check that cancellation closes the connection instead of waiting for the
// read deadline.
type blockingConn struct {
	stubConn
	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newBlockingConn() *blockingConn {
	return &blockingConn{started: make(chan struct{}, 1), done: make(chan struct{})}
}

func (c *blockingConn) Read([]byte) (int, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.done
	return 0, io.EOF
}

func (c *blockingConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.stubConn.Close()
}

// manualClock advances only when the fake dial does, so budget decisions are
// deterministic instead of depending on wall-clock scheduling.
type manualClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
}

func transportRequests(records []contract.Record) []contract.ProbeRequest {
	reqs := make([]contract.ProbeRequest, 0, len(records))
	for _, r := range records {
		reqs = append(reqs, contract.ProbeRequest{
			Kind: contract.ProbeTransport, Exit: contract.ExitID(r.ID),
			Trigger: contract.TriggerRotate,
		})
	}
	return reqs
}

func TestRotationCycleBudgetFitsInsideTheInterval(t *testing.T) {
	if got := contract.HealthCycleBudget + contract.ProbeTimeout; got >= contract.HealthProbeInterval {
		t.Fatalf("cycle budget plus one probe timeout = %s, want less than the %s interval", got, contract.HealthProbeInterval)
	}
}

func TestRotationCycleCapsConfiguredTimeout(t *testing.T) {
	e := NewEngine(Options{
		Timeout:          time.Hour,
		RotationInterval: time.Minute,
		RotationBudget:   20 * time.Second,
	})
	defer e.Close()
	if got, limit := e.batchTimeout(), (time.Minute-20*time.Second)/2; got <= 0 || got > limit {
		t.Fatalf("batch timeout = %s, want it capped to %s", got, limit)
	}

	def := NewEngine(Options{})
	defer def.Close()
	if got := def.batchTimeout(); got != contract.ProbeTimeout {
		t.Fatalf("default batch timeout = %s, want %s", got, contract.ProbeTimeout)
	}
}

func TestRotationCycleUsesCappedTimeout(t *testing.T) {
	records := []contract.Record{rotationRecord("x-a", 0)}
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	opts := baseOptions(records, dial)
	opts.Clock = time.Now
	opts.HealthConcurrent = 1
	opts.Timeout = time.Hour
	opts.RotationInterval = 2 * time.Second
	opts.RotationBudget = time.Second
	e := NewEngine(opts)
	defer e.Close()

	start := time.Now()
	result := e.RunBatch(context.Background(), transportRequests(records))
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cycle took %s, want the batch timeout cap (500ms) not the 1h configured timeout", elapsed)
	}
	if result.Started != 1 || result.Remaining != 0 {
		t.Fatalf("started=%d remaining=%d, want the single probe", result.Started, result.Remaining)
	}
}

func TestRotationSkipsUndialableRecords(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	records := make([]contract.Record, 0, 153)
	for i := 0; i < 150; i++ {
		r := rotationRecord("x-bad-"+strconv.Itoa(i), i)
		if i%2 == 0 {
			r.Status = contract.RecordUnsupported
		} else {
			r.Protocol = contract.Protocol("http")
		}
		records = append(records, r)
	}
	want := []contract.ExitID{"x-ok-a", "x-ok-b", "x-ok-c"}
	for i, id := range want {
		records = append(records, rotationRecord(string(id), 1000+i))
	}
	opts := baseOptions(records, nil)
	opts.Clock = func() time.Time { return now }
	opts.Targets = []string{"a.example.com:443"}
	e := NewEngine(opts)
	defer e.Close()

	batch := e.Rotate(now)
	if got := transportExits(batch); len(got) != len(want) {
		t.Fatalf("rotation transports = %v, want only the dialable records %v", got, want)
	}
	for i, exit := range transportExits(batch) {
		if exit != want[i] {
			t.Fatalf("rotation transports = %v, want %v", transportExits(batch), want)
		}
	}
	for _, req := range batch {
		if req.Kind == contract.ProbeEgress && req.Exit != want[0] {
			t.Fatalf("egress exit = %s, want a dialable record", req.Exit)
		}
	}
}

func TestRotationCycleBudgetLeavesRemainderUnseen(t *testing.T) {
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	records := make([]contract.Record, 0, 6)
	for i := 0; i < 6; i++ {
		records = append(records, rotationRecord("x-"+string(rune('a'+i)), i))
	}
	clock := &manualClock{now: start, step: 600 * time.Millisecond}
	health := map[contract.ExitID]contract.ExitHealth{}
	opts := baseOptions(records, func(context.Context, string, string) (net.Conn, error) {
		clock.Advance()
		return &stubConn{}, nil
	})
	opts.Clock = clock.Now
	opts.Health = func(id contract.ExitID) (contract.ExitHealth, bool) {
		h, ok := health[id]
		return h, ok
	}
	opts.HealthConcurrent = 1
	opts.RotationBudget = time.Second
	e := NewEngine(opts)
	defer e.Close()

	unseen := len(records)
	for cycle := 0; unseen > 0; cycle++ {
		if cycle > len(records) {
			t.Fatalf("unseen stopped at %d after %d cycles", unseen, cycle)
		}
		at := start.Add(time.Duration(cycle) * contract.HealthProbeInterval)
		reqs := e.Rotate(at)
		result := e.RunBatch(context.Background(), reqs)
		if result.Started != 2 {
			t.Fatalf("cycle %d started %d probes, want 2 inside the simulated budget", cycle, result.Started)
		}
		if result.Remaining != len(reqs)-result.Started {
			t.Fatalf("cycle %d remaining = %d, want %d", cycle, result.Remaining, len(reqs)-result.Started)
		}
		for _, m := range result.Measurements {
			health[m.Result.Exit] = contract.ExitHealth{
				Exit: m.Result.Exit, Status: contract.HealthUnknown, LastSuccessAt: at,
			}
		}
		next := 0
		for _, r := range records {
			if _, ok := health[contract.ExitID(r.ID)]; !ok {
				next++
			}
		}
		if next >= unseen {
			t.Fatalf("cycle %d unseen = %d, want fewer than %d", cycle, next, unseen)
		}
		unseen = next
	}
}

func TestRotationCycleInterleavesEgressSlotsUnderCutoff(t *testing.T) {
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	targetA, ok := ParseTarget("a.example.com:443")
	if !ok {
		t.Fatal("target a did not parse")
	}
	targetB, ok := ParseTarget("b.example.com:443")
	if !ok {
		t.Fatal("target b did not parse")
	}
	reqs := []contract.ProbeRequest{
		{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerRotate},
		{Kind: contract.ProbeTransport, Exit: "x-b", Trigger: contract.TriggerRotate},
		{Kind: contract.ProbeEgress, Exit: "x-a", Destination: targetA, Trigger: contract.TriggerRotate},
		{Kind: contract.ProbeEgress, Exit: "x-b", Destination: targetB, Trigger: contract.TriggerRotate},
	}
	records := []contract.Record{rotationRecord("x-a", 0), rotationRecord("x-b", 1)}
	newCycle := func(budget time.Duration, calls *atomic.Int32) (*Engine, *manualClock) {
		clock := &manualClock{now: start, step: 600 * time.Millisecond}
		opts := baseOptions(records, func(context.Context, string, string) (net.Conn, error) {
			clock.Advance()
			return &stubConn{}, nil
		})
		opts.Clock = clock.Now
		opts.Targets = []string{"a.example.com:443", "b.example.com:443"}
		opts.Pool = fakePool{fn: func(_ context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
			calls.Add(1)
			clock.Advance()
			return contract.PoolDialResult{Conn: &stubConn{data: []byte{1}}, Exit: req.Exit}, nil
		}}
		opts.HealthConcurrent = 1
		opts.RotationBudget = budget
		e := NewEngine(opts)
		t.Cleanup(e.Close)
		return e, clock
	}

	var calls atomic.Int32
	e, _ := newCycle(time.Second, &calls)
	result := e.RunBatch(context.Background(), reqs)
	if result.Started != 2 || result.Remaining != 2 {
		t.Fatalf("cutoff batch = %d started, %d remaining, want 2 and 2", result.Started, result.Remaining)
	}
	if calls.Load() != 1 {
		t.Fatalf("egress dials = %d, want one egress slot before transports consume the budget", calls.Load())
	}
	kinds := map[contract.ProbeKind]int{}
	for _, m := range result.Measurements {
		kinds[m.Result.Kind]++
	}
	if kinds[contract.ProbeTransport] != 1 || kinds[contract.ProbeEgress] != 1 {
		t.Fatalf("cutoff kinds = %v, want one transport and one egress", kinds)
	}

	var fullCalls atomic.Int32
	full, _ := newCycle(10*time.Second, &fullCalls)
	fullResult := full.RunBatch(context.Background(), reqs)
	if fullResult.Started != 4 || fullResult.Remaining != 0 || fullCalls.Load() != 2 {
		t.Fatalf("full batch = %d started, %d remaining, %d egress dials, want all four and two egress",
			fullResult.Started, fullResult.Remaining, fullCalls.Load())
	}
}

func TestRotationCycleRespectsSharedHealthConcurrency(t *testing.T) {
	user := rec("x-user", contract.TransportRaw, contract.SecurityNone)
	user.Server, user.Port = "user.invalid", 1
	batchRecords := make([]contract.Record, 0, 6)
	for i := 0; i < 6; i++ {
		batchRecords = append(batchRecords, rotationRecord("x-"+string(rune('a'+i)), i))
	}
	all := append([]contract.Record{user}, batchRecords...)

	userRelease := make(chan struct{})
	batchRelease := make(chan struct{})
	var userOnce, batchOnce sync.Once
	releaseUser := func() { userOnce.Do(func() { close(userRelease) }) }
	releaseBatch := func() { batchOnce.Do(func() { close(batchRelease) }) }
	defer releaseUser()
	defer releaseBatch()
	userStarted := make(chan struct{})
	batchStarted := make(chan struct{}, 2)
	var active, peak atomic.Int64
	dial := func(ctx context.Context, _ string, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "user.invalid:") {
			close(userStarted)
			<-userRelease
			return &stubConn{}, nil
		}
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case batchStarted <- struct{}{}:
		default:
		}
		select {
		case <-batchRelease:
		case <-ctx.Done():
			active.Add(-1)
			return nil, ctx.Err()
		}
		active.Add(-1)
		return &stubConn{}, nil
	}
	opts := baseOptions(all, dial)
	opts.HealthConcurrent = 2
	opts.Timeout = 2 * time.Second
	opts.RotationBudget = time.Minute
	e := NewEngine(opts)
	defer e.Close()

	// Hold one health slot with a user-triggered probe; the batch must then
	// run one probe at a time, not two.
	userDone := make(chan error, 1)
	go func() {
		_, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
			Kind: contract.ProbeTransport, Exit: "x-user", Trigger: contract.TriggerManual,
		})
		userDone <- err
	}()
	waitSignal(t, userStarted, "the user probe dial")

	resultCh := make(chan BatchResult, 1)
	go func() { resultCh <- e.RunBatch(context.Background(), transportRequests(batchRecords)) }()
	waitSignal(t, batchStarted, "a batch dial")
	time.Sleep(50 * time.Millisecond)
	if got := peak.Load(); got != 1 {
		t.Fatalf("batch peak concurrency while a user probe held a slot = %d, want 1", got)
	}
	releaseUser()
	if err := <-userDone; err != nil {
		t.Fatalf("user probe: %v", err)
	}
	releaseBatch()
	result := <-resultCh

	if peak.Load() > 2 {
		t.Fatalf("peak probe concurrency = %d, want at most the semaphore limit 2", peak.Load())
	}
	if result.Started != len(batchRecords) || len(result.Measurements) != len(batchRecords) {
		t.Fatalf("batch = %d started, %d measured, want %d", result.Started, len(result.Measurements), len(batchRecords))
	}
}

func TestRotationCycleBudgetStopsSlowFakes(t *testing.T) {
	records := make([]contract.Record, 0, 10)
	for i := 0; i < 10; i++ {
		records = append(records, rotationRecord("x-"+string(rune('a'+i)), i))
	}
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	opts := baseOptions(records, dial)
	opts.HealthConcurrent = 2
	opts.Timeout = 80 * time.Millisecond
	opts.RotationBudget = 200 * time.Millisecond
	e := NewEngine(opts)
	defer e.Close()

	started := time.Now()
	result := e.RunBatch(context.Background(), transportRequests(records))
	elapsed := time.Since(started)
	if result.Started == 0 || result.Started == len(records) {
		t.Fatalf("started = %d, want a partial cycle inside the budget", result.Started)
	}
	if result.Remaining != len(records)-result.Started {
		t.Fatalf("remaining = %d, want %d", result.Remaining, len(records)-result.Started)
	}
	if elapsed > opts.RotationBudget+opts.Timeout+time.Second {
		t.Fatalf("cycle took %s, want at most budget plus one probe timeout", elapsed)
	}
}

func TestRotationCycleCancelStopsPromptly(t *testing.T) {
	records := make([]contract.Record, 0, 8)
	for i := 0; i < 8; i++ {
		records = append(records, rotationRecord("x-"+string(rune('a'+i)), i))
	}
	var opened, active atomic.Int64
	dialStarted := make(chan struct{}, 2)
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		opened.Add(1)
		active.Add(1)
		select {
		case dialStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		active.Add(-1)
		return nil, ctx.Err()
	}
	opts := baseOptions(records, dial)
	opts.HealthConcurrent = 2
	opts.Timeout = time.Hour
	opts.RotationBudget = time.Hour
	e := NewEngine(opts)
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan BatchResult, 1)
	go func() { resultCh <- e.RunBatch(ctx, transportRequests(records)) }()
	waitSignal(t, dialStarted, "the first in-flight dial")
	waitSignal(t, dialStarted, "the second in-flight dial")
	cancel()

	select {
	case result := <-resultCh:
		if result.Started != 2 {
			t.Fatalf("started after cancel = %d, want the two in-flight probes", result.Started)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunBatch did not return promptly after cancellation")
	}
	if active.Load() != 0 {
		t.Fatalf("dial still active after cancel: %d", active.Load())
	}
	count := opened.Load()
	time.Sleep(20 * time.Millisecond)
	if now := opened.Load(); now != count || active.Load() != 0 {
		t.Fatalf("canceled cycle kept dialing: %d -> %d", count, now)
	}
}

func TestRotationCycleCancelClosesEgressRead(t *testing.T) {
	target, ok := ParseTarget("a.example.com:443")
	if !ok {
		t.Fatal("target did not parse")
	}
	records := []contract.Record{rotationRecord("x-a", 0)}
	conn := newBlockingConn()
	opts := baseOptions(records, nil)
	opts.Targets = []string{"a.example.com:443"}
	opts.Pool = fakePool{fn: func(_ context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
		return contract.PoolDialResult{Conn: conn, Exit: req.Exit}, nil
	}}
	opts.HealthConcurrent = 2
	opts.Timeout = time.Hour
	opts.RotationBudget = time.Hour
	e := NewEngine(opts)
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan BatchResult, 1)
	go func() {
		resultCh <- e.RunBatch(ctx, []contract.ProbeRequest{{
			Kind: contract.ProbeEgress, Exit: "x-a", Destination: target,
			Trigger: contract.TriggerRotate,
		}})
	}()
	waitSignal(t, conn.started, "the egress first-byte read")
	cancel()
	select {
	case result := <-resultCh:
		if result.Started != 1 {
			t.Fatalf("started = %d, want the in-flight egress probe", result.Started)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunBatch waited for the first-byte deadline after cancellation")
	}
	if conn.closed.Load() == 0 {
		t.Fatal("canceled egress read left the connection open")
	}
}
