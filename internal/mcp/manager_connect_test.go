package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// fakeDialer stands in for real transports: every dial returns a fakeConn
// whose handshake can be held open until release, so tests can line up
// overlapping connects without sleeping. A server's behaviour is keyed by its
// config's Command.
type fakeDialer struct {
	mu          sync.Mutex
	conns       []*fakeConn
	inFlight    int
	maxInFlight int

	release chan struct{} // nil: handshakes finish at once
	entered chan string   // receives a server's Command as its handshake starts

	initErr map[string]error
}

func newFakeDialer(hold bool) *fakeDialer {
	d := &fakeDialer{entered: make(chan string, 64), initErr: map[string]error{}}
	if hold {
		d.release = make(chan struct{})
	}
	return d
}

func (d *fakeDialer) dial(_ context.Context, cfg ServerConfig, _ string, _ func(string) string) (Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := &fakeConn{d: d, key: cfg.Command, id: len(d.conns) + 1}
	d.conns = append(d.conns, c)
	return c, nil
}

// hold makes handshakes that start from now on wait for releaseAll.
func (d *fakeDialer) hold() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.release = make(chan struct{})
}

func (d *fakeDialer) releaseAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.release != nil {
		close(d.release)
		d.release = nil
	}
}

func (d *fakeDialer) created() []*fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*fakeConn(nil), d.conns...)
}

func (d *fakeDialer) peakHandshakes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxInFlight
}

type fakeConn struct {
	d      *fakeDialer
	key    string
	id     int
	closes atomic.Int32
	called atomic.Value // last tool name passed to CallTool
}

func (c *fakeConn) Initialize(ctx context.Context) (string, error) {
	c.d.mu.Lock()
	c.d.inFlight++
	c.d.maxInFlight = max(c.d.maxInFlight, c.d.inFlight)
	err := c.d.initErr[c.key]
	release := c.d.release
	c.d.mu.Unlock()
	defer func() {
		c.d.mu.Lock()
		c.d.inFlight--
		c.d.mu.Unlock()
	}()
	c.d.entered <- c.key
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	return "use " + c.key + " wisely", nil
}

func (c *fakeConn) ListTools(context.Context) ([]ToolDef, error) {
	return []ToolDef{{Name: "ping", Description: "ping " + c.key}}, nil
}

func (c *fakeConn) CallTool(_ context.Context, name string, _ json.RawMessage) (CallResult, error) {
	c.called.Store(name)
	return CallResult{Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("pong from %d", c.id)}}}, nil
}

func (c *fakeConn) ListResources(context.Context) ([]ResourceDef, error) { return nil, nil }
func (c *fakeConn) ReadResource(context.Context, string) ([]ResourceContent, error) {
	return nil, nil
}
func (c *fakeConn) Close() error {
	c.closes.Add(1)
	return nil
}

// connectWithCwd connects one server from the config given, under its connect
// lock, as in a trusted workspace. Tests use it to skip config loading.
func (m *Manager) connectWithCwd(ctx context.Context, name string, cfg ServerConfig, cwd string) {
	lock := m.connLock(name)
	lock.Lock()
	defer lock.Unlock()
	m.connectLocked(ctx, name, cfg, cwd, true)
}

// isolateConfig points every MCP and settings source at fresh temp dirs and
// returns a project directory to use as cwd.
func isolateConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	return t.TempDir()
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeUserServers writes servers to conduit's user MCP file.
func writeUserServers(t *testing.T, servers map[string]ServerConfig) {
	t.Helper()
	writeJSON(t, conduitMCPFile(), McpJSON{McpServers: servers})
}

// writeProjectServers writes servers to cwd's .mcp.json (project scope).
func writeProjectServers(t *testing.T, cwd string, servers map[string]ServerConfig) {
	t.Helper()
	writeJSON(t, filepath.Join(cwd, ".mcp.json"), McpJSON{McpServers: servers})
}

func statusOf(m *Manager, name string) ServerStatus {
	srv, ok := m.Server(name)
	if !ok {
		return "absent"
	}
	return srv.Status
}

// The startup connect must not hold up the TUI: StartConnectAll returns while
// handshakes are still running, with every server already listed. Gating
// needs no network, so it is settled by then — the approval prompt at startup
// reads PendingApprovals immediately.
func TestStartConnectAll_RecordsStartingStatesBeforeHandshakesFinish(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{
		"slow": {Command: "slow"},
		"off":  {Command: "off"},
	})
	writeProjectServers(t, cwd, map[string]ServerConfig{
		"proj":   {Command: "proj"},
		"denied": {Command: "denied"},
	})
	if err := settings.ApproveMcpjsonServer("denied", "no", cwd); err != nil {
		t.Fatal(err)
	}
	if err := SetDisabled("off", cwd, true); err != nil {
		t.Fatal(err)
	}

	d := newFakeDialer(true)
	t.Cleanup(d.releaseAll)
	m := NewManager()
	m.dial = d.dial
	if err := m.StartConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatalf("StartConnectAll: %v", err)
	}
	<-d.entered // slow's handshake is under way and held open

	tests := []struct {
		name     string
		status   ServerStatus
		disabled bool
		errHas   string
	}{
		{"slow", StatusPending, false, ""},
		{"off", StatusDisconnected, true, ""},
		{"proj", StatusNeedsApproval, false, ""},
		{"denied", StatusDisconnected, false, "denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, ok := m.Server(tt.name)
			if !ok {
				t.Fatalf("%s not listed while connects run", tt.name)
			}
			if srv.Status != tt.status || srv.Disabled != tt.disabled {
				t.Errorf("status = %q disabled = %v, want %q / %v", srv.Status, srv.Disabled, tt.status, tt.disabled)
			}
			if tt.errHas != "" && !strings.Contains(srv.Error, tt.errHas) {
				t.Errorf("error = %q, want it to mention %q", srv.Error, tt.errHas)
			}
		})
	}
	if got := m.PendingApprovals(); len(got) != 1 || got[0] != "proj" {
		t.Errorf("PendingApprovals = %v, want [proj]: a denied server must not be asked about again", got)
	}

	d.releaseAll()
	if err := m.WaitForInit(context.Background(), InitWaitBudget); err != nil {
		t.Fatalf("WaitForInit: %v", err)
	}
	if got := statusOf(m, "slow"); got != StatusConnected {
		t.Errorf("slow = %q after its handshake, want connected", got)
	}
	if got := m.Cwd(); got != cwd {
		t.Errorf("Cwd = %q, want %q", got, cwd)
	}
}

// Everything that changes a server's state has to reach the change hook: the
// tool registry is kept in step through it.
func TestManager_OnChangeSeesEveryTransition(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{"a": {Command: "a"}})
	d := newFakeDialer(false)
	m := NewManager()
	m.dial = d.dial
	var seen []ServerStatus
	m.SetOnChange(func() { seen = append(seen, statusOf(m, "a")) })

	steps := []struct {
		name string
		act  func() error
		want []ServerStatus
	}{
		{"startup connect", func() error { return m.ConnectAll(context.Background(), cwd, true) },
			[]ServerStatus{StatusPending, StatusConnected}},
		{"reconnect", func() error { return m.Reconnect(context.Background(), "a", cwd, true) },
			[]ServerStatus{StatusPending, StatusConnected}},
		{"disable", func() error {
			if err := SetDisabled("a", cwd, true); err != nil {
				return err
			}
			return m.Reconnect(context.Background(), "a", cwd, true)
		}, []ServerStatus{StatusDisconnected}},
		{"disconnect", func() error { m.DisconnectServer("a"); return nil },
			[]ServerStatus{"absent"}},
	}
	for _, st := range steps {
		seen = nil
		if err := st.act(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if fmt.Sprint(seen) != fmt.Sprint(st.want) {
			t.Errorf("%s: hook saw %v, want %v", st.name, seen, st.want)
		}
	}
}

// A reconnect issued while the startup connect of the same server is still
// in its handshake must wait for it, then replace it: one live client at the
// end, the reconnect's, and the startup one closed.
func TestManager_ReconnectWaitsForInFlightConnect(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{"srv": {Command: "srv"}})
	d := newFakeDialer(true)
	t.Cleanup(d.releaseAll)
	m := NewManager()
	m.dial = d.dial
	if err := m.StartConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	<-d.entered // the startup handshake is held open

	if lock := m.connLock("srv"); lock.TryLock() {
		lock.Unlock()
		t.Fatal("connect lock not held across the handshake: a reconnect could open a second client alongside it")
	}
	var changes atomic.Int32
	m.SetOnChange(func() { changes.Add(1) })
	errc := make(chan error, 1)
	go func() { errc <- m.Reconnect(context.Background(), "srv", cwd, true) }()
	// While the startup handshake holds the server, the reconnect must not
	// record a state or start a handshake of its own. It has no event to
	// wait on when it is correctly blocked, so give it a moment to misbehave.
	select {
	case key := <-d.entered:
		t.Fatalf("reconnect started a handshake of %s while the startup one held the server", key)
	case <-time.After(50 * time.Millisecond):
	}
	if n := changes.Load(); n != 0 {
		t.Fatalf("reconnect changed the server's state %d times while the startup connect held it", n)
	}
	d.releaseAll()
	if err := <-errc; err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if err := m.WaitForInit(context.Background(), InitWaitBudget); err != nil {
		t.Fatal(err)
	}

	conns := d.created()
	if len(conns) != 2 {
		t.Fatalf("dialed %d clients, want 2 (startup + reconnect)", len(conns))
	}
	startup, reconnect := conns[0], conns[1]
	if n := startup.closes.Load(); n != 1 {
		t.Errorf("startup client closed %d times, want 1", n)
	}
	if n := reconnect.closes.Load(); n != 0 {
		t.Errorf("reconnect client closed %d times, want 0 (it is the live one)", n)
	}
	if peak := d.peakHandshakes(); peak != 1 {
		t.Errorf("%d handshakes of one server overlapped, want 1", peak)
	}
	if _, err := m.CallTool(context.Background(), "mcp__srv__ping", nil); err != nil {
		t.Fatal(err)
	}
	if reconnect.called.Load() != "ping" {
		t.Error("tool call did not reach the reconnect's client")
	}
}

// Installing several plugins at once runs one SyncPluginServers per plugin;
// they must not both connect the same new server.
func TestSyncPluginServers_ConcurrentSyncsDialOnce(t *testing.T) {
	cwd := isolateConfig(t)
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	pluginDir := filepath.Join(claudeDir, "plugins", "cache", "ctx")
	writeJSON(t, filepath.Join(pluginDir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]ServerConfig{"ctx": {Command: "ctx"}},
	})
	writeJSON(t, filepath.Join(claudeDir, "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"ctx@claude-plugins-official": []map[string]any{{"scope": "user", "installPath": pluginDir, "version": "1.0.0"}},
		},
	})

	d := newFakeDialer(true)
	t.Cleanup(d.releaseAll)
	m := NewManager()
	m.dial = d.dial
	var wg sync.WaitGroup
	wg.Go(func() { m.SyncPluginServers(context.Background(), cwd, true) })
	<-d.entered // the first sync is mid-handshake, holding the server
	wg.Go(func() { m.SyncPluginServers(context.Background(), cwd, true) })
	d.releaseAll()
	wg.Wait()

	if n := len(d.created()); n != 1 {
		t.Errorf("dialed %d clients for one plugin server, want 1", n)
	}
	if got := statusOf(m, "plugin:ctx:ctx"); got != StatusConnected {
		t.Errorf("plugin server = %q, want connected", got)
	}
}

func TestManager_ReconnectRemovesServerDroppedFromConfig(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{"gone": {Command: "gone"}})
	d := newFakeDialer(false)
	m := NewManager()
	m.dial = d.dial
	if err := m.ConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	writeUserServers(t, map[string]ServerConfig{})

	err := m.Reconnect(context.Background(), "gone", cwd, true)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Reconnect err = %v, want not found", err)
	}
	if _, ok := m.Server("gone"); ok {
		t.Error("server dropped from config is still listed")
	}
	if n := d.created()[0].closes.Load(); n != 1 {
		t.Errorf("client closed %d times, want 1", n)
	}
}

// A connect that finishes after Close must not leave its client running.
func TestManager_CloseDuringConnectClosesLateClient(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{"late": {Command: "late"}})
	d := newFakeDialer(true)
	t.Cleanup(d.releaseAll)
	m := NewManager()
	m.dial = d.dial
	if err := m.StartConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	<-d.entered
	m.Close()
	d.releaseAll()
	if err := m.WaitForInit(context.Background(), InitWaitBudget); err != nil {
		t.Fatal(err)
	}
	if n := d.created()[0].closes.Load(); n != 1 {
		t.Errorf("late client closed %d times, want 1", n)
	}
	if got := statusOf(m, "late"); got == StatusConnected {
		t.Error("server reported connected after Close")
	}
}

func TestManager_WaitForInit(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		started time.Time // zero: StartConnectAll never ran
		status  ServerStatus
		ctx     context.Context
		budget  time.Duration
		wantErr error
	}{
		{"never started", time.Time{}, StatusPending, context.Background(), time.Hour, nil},
		{"nothing connecting", time.Now(), StatusConnected, context.Background(), time.Hour, nil},
		{"budget spent since start", time.Now().Add(-time.Minute), StatusPending, context.Background(), 30 * time.Second, nil},
		{"caller cancelled", time.Now(), StatusPending, cancelled, time.Hour, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManagerWithServers(map[string]*ConnectedServer{"s": {Name: "s", Status: tt.status}})
			m.initStarted = tt.started
			err := m.WaitForInit(tt.ctx, tt.budget)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("WaitForInit = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// waitingCtx reports, by closing waiting, when a waiter first asks for its
// Done channel. WaitForInit does that only once it has read the manager's
// state and is about to block, so a change made after waiting closes has to
// wake it.
type waitingCtx struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func newWaitingCtx() *waitingCtx {
	return &waitingCtx{Context: context.Background(), waiting: make(chan struct{})}
}

func (c *waitingCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// The budget is a ceiling, not a delay: a wait ends as soon as nothing is
// connecting any more.
func TestManager_WaitForInitReturnsWhenConnectsFinish(t *testing.T) {
	m := NewManagerWithServers(map[string]*ConnectedServer{"s": {Name: "s", Status: StatusPending}})
	m.initStarted = time.Now()
	ctx := newWaitingCtx()
	errc := make(chan error, 1)
	go func() { errc <- m.WaitForInit(ctx, time.Hour) }()
	<-ctx.waiting
	m.store("s", &ConnectedServer{Name: "s", Status: StatusConnected})
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("WaitForInit = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForInit kept waiting after the connect finished")
	}
}

func TestManager_ApprovalDecided(t *testing.T) {
	tests := []struct {
		name       string
		start      ServerStatus // "" = not listed
		approved   bool
		want       ServerStatus
		wantNotify int
	}{
		{"approved", StatusNeedsApproval, true, StatusPending, 1},
		{"denied", StatusNeedsApproval, false, StatusDisconnected, 1},
		{"not awaiting approval", StatusConnected, false, StatusConnected, 0},
		{"unknown server", "", true, "absent", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			servers := map[string]*ConnectedServer{}
			if tt.start != "" {
				servers["p"] = &ConnectedServer{Name: "p", Status: tt.start, Config: ServerConfig{Scope: "project"}}
			}
			m := NewManagerWithServers(servers)
			notified := 0
			m.SetOnChange(func() { notified++ })
			m.ApprovalDecided("p", tt.approved)
			if got := statusOf(m, "p"); got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
			if notified != tt.wantNotify {
				t.Errorf("hook ran %d times, want %d", notified, tt.wantNotify)
			}
			if pending := m.PendingApprovals(); len(pending) != 0 {
				t.Errorf("PendingApprovals = %v after the decision, want none", pending)
			}
		})
	}
}

func TestManager_ServerLookup(t *testing.T) {
	m := NewManagerWithServers(map[string]*ConnectedServer{"x": {Name: "x", Status: StatusFailed}})
	if srv, ok := m.Server("x"); !ok || srv.Status != StatusFailed {
		t.Errorf("Server(x) = %+v, %v", srv, ok)
	}
	if _, ok := m.Server("y"); ok {
		t.Error("Server(y) found a server that was never added")
	}
}

// The first prompt waits for servers still connecting at startup, and that
// includes one the user approved at the startup prompt: its connect runs
// through Reconnect, outside the startup fan-out, but the prompt is no less
// likely to need its tools.
func TestManager_WaitForInitWaitsForApprovedServer(t *testing.T) {
	cwd := isolateConfig(t)
	writeUserServers(t, map[string]ServerConfig{"fast": {Command: "fast"}})
	writeProjectServers(t, cwd, map[string]ServerConfig{"proj": {Command: "proj"}})
	d := newFakeDialer(false)
	t.Cleanup(d.releaseAll)
	m := NewManager()
	m.dial = d.dial
	ctx := context.Background()
	if err := m.StartConnectAll(ctx, cwd, true); err != nil {
		t.Fatal(err)
	}
	if err := m.WaitForInit(ctx, InitWaitBudget); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(m, "fast"); got != StatusConnected {
		t.Fatalf("fast = %q after the startup wait", got)
	}

	// The user answers "yes" at the startup prompt, as /mcp-approve does.
	if err := settings.ApproveMcpjsonServer("proj", "yes", cwd); err != nil {
		t.Fatal(err)
	}
	d.hold()
	m.ApprovalDecided("proj", true)
	errc := make(chan error, 1)
	go func() { errc <- m.Reconnect(ctx, "proj", cwd, true) }()
	for key := <-d.entered; key != "proj"; key = <-d.entered {
	}

	// With the budget about to run out, the prompt waits it out.
	setStarted := func(at time.Time) {
		m.mu.Lock()
		m.initStarted = at
		m.mu.Unlock()
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	setStarted(deadline.Add(-InitWaitBudget))
	if err := m.WaitForInit(ctx, InitWaitBudget); err != nil {
		t.Fatal(err)
	}
	if early := time.Until(deadline); early > 0 {
		t.Fatalf("WaitForInit returned %v before its deadline while proj was still connecting", early)
	}

	// With budget left, the wait ends as soon as the server connects.
	setStarted(time.Now())
	waitCtx := newWaitingCtx()
	statusAtReturn := make(chan ServerStatus, 1)
	go func() {
		if err := m.WaitForInit(waitCtx, InitWaitBudget); err != nil {
			t.Error(err)
		}
		statusAtReturn <- statusOf(m, "proj")
	}()
	<-waitCtx.waiting
	d.releaseAll()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-statusAtReturn:
		if got != StatusConnected {
			t.Errorf("WaitForInit returned with proj %q, want connected", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForInit kept waiting after proj connected")
	}
}
