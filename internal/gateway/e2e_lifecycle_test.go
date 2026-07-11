package gateway

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Forest-Isle/daimon/internal/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nullChannel is a minimal channel.Channel used to exercise the full
// Start()/Stop() lifecycle without any external dependency.
type nullChannel struct {
	started bool
	stopped bool
}

func (c *nullChannel) Name() string { return "null" }
func (c *nullChannel) Start(ctx context.Context, handler channel.InboundHandler) error {
	c.started = true
	return nil
}
func (c *nullChannel) Stop(ctx context.Context) error {
	c.stopped = true
	return nil
}
func (c *nullChannel) Send(ctx context.Context, msg channel.OutboundMessage) error {
	return nil
}
func (c *nullChannel) SendStreaming(ctx context.Context, target channel.MessageTarget) (channel.StreamUpdater, error) {
	return nil, nil
}

// TestGatewayFullLifecycle exercises the complete runtime wiring:
// New() constructs every subsystem, Start() boots channels + scheduler +
// health server + background goroutines, and Stop() tears them down cleanly.
// This covers the runtime paths that unit tests on individual subsystems miss.
func TestGatewayFullLifecycle(t *testing.T) {
	cfg := testConfig(t)
	// Enable memory so the memory-backed tools (core_memory, amp_memory) wire up.
	cfg.Memory.Enabled = true
	gw, err := New(cfg)
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			require.NoError(t, gw.Stop(context.Background()))
		}
	})

	// Every core subsystem must be wired after New().
	assert.NotNil(t, gw.agent, "agent runtime")
	assert.NotNil(t, gw.toolSub.Registry, "tool registry")
	assert.NotNil(t, gw.sessions, "session manager")
	assert.NotNil(t, gw.features, "feature registry")
	assert.NotNil(t, gw.health.registry, "health registry")

	// Core tools must be registered end-to-end.
	_, err = gw.toolSub.Registry.Get("memory")
	assert.NoError(t, err, "memory tool must be registered")

	ch := &nullChannel{}
	gw.AddChannel(ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, gw.Start(ctx), "Start must succeed")
	assert.True(t, ch.started, "channel must be started")

	// Health report must include the database checker and report OK overall.
	hctx, hcancel := context.WithTimeout(ctx, 2*time.Second)
	defer hcancel()
	report := gw.health.registry.Check(hctx)
	require.Contains(t, report.Checks, "database")
	assert.Equal(t, HealthOK, report.Checks["database"].Status, "database health check")

	require.NoError(t, gw.Stop(context.Background()), "Stop must succeed")
	stopped = true
}

func TestGatewayAdminLifecycle(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.Enabled = true
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.Token = "test-admin-token"

	gw, err := New(cfg)
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			require.NoError(t, gw.Stop(context.Background()))
		}
	})
	require.NotNil(t, gw.admin, "admin subsystem must be wired")
	assert.Nil(t, gw.admin.listener, "admin listener must not bind during construction")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, gw.Start(ctx))
	require.NotNil(t, gw.admin.listener, "admin listener must bind during Start")

	require.NoError(t, gw.Stop(context.Background()))
	stopped = true
	assert.Nil(t, gw.admin.listener, "admin listener must be cleared during Stop")
}

func TestGatewayAdminBindFailureIsSynchronous(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	cfg := testConfig(t)
	cfg.Server.Enabled = true
	cfg.Server.Addr = ln.Addr().String()
	cfg.Server.Token = "test-admin-token"

	gw, err := New(cfg)
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			require.NoError(t, gw.Stop(context.Background()))
		}
	})
	ch := &nullChannel{}
	gw.AddChannel(ch)

	err = gw.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin:")
	assert.False(t, ch.started, "channels must not start after an admin bind failure")

	require.NoError(t, gw.Stop(context.Background()))
	stopped = true
}

func TestGatewayRejectsAdminWithoutToken(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.Enabled = true
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.Token = ""

	gw, err := New(cfg)
	require.Error(t, err)
	assert.Nil(t, gw)
	assert.Contains(t, err.Error(), "admin: server.token")
}
