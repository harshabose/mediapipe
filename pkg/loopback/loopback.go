// Package loopback provides a bidirectional UDP-based bridge that demonstrates
// the power of the universal media routing system by implementing both
// CanGenerate and CanConsume interfaces in a single component.
//
// # The LoopBack Bridge Concept
//
// LoopBack represents a network bridge that can simultaneously act as both
// a data source and data sink, enabling complex media routing topologies
// where data can flow through network boundaries and back into the same
// pipeline or different pipelines entirely.
//
// # Bidirectional Architecture
//
// The LoopBack implements both fundamental interfaces of the media routing system:
//
//	Input Side:  UDP Socket → LoopBack.Read() → mediapipe.Reader → Pipeline
//	Output Side: Pipeline → mediapipe.Writer → LoopBack.Consume() → UDP Socket
//
// This bidirectional nature enables powerful use cases:
//   - Network-based media routing between different systems
//   - Load balancing across multiple processing pipelines
//   - Remote media processing with local consumption
//   - Testing and simulation of distributed media systems
//   - Bridge between different network protocols or formats
//
// # Real-World Applications
//
// The LoopBack pattern enables sophisticated media routing scenarios:
//
//	WebRTC → LoopBack → Remote Processing → LoopBack → RTSP Serve
//	MAVLINK → LoopBack → Cloud Analytics → LoopBack → Ground Station
//	Audio Stream → LoopBack → Remote Effects → LoopBack → Live Broadcast
//
// Each LoopBack can be on different machines, different networks, or even
// different cloud regions, while maintaining the same clean interface
// integration with the universal media routing system.
//
// # Built-in Observability
//
// LoopBack includes comprehensive metrics and monitoring capabilities,
// making it production-ready for real-world deployments where visibility
// into data flow rates and network performance is critical. Metrics
// collection starts automatically upon creation.
package loopback

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type metrics struct {
	DataSent      int64   // Total bytes sent since startup
	DataRecvd     int64   // Total bytes received since startup
	DataSentRate  float32 // Current send rate in bytes per second
	DataRecvdRate float32 // Current receive rate in bytes per second

	// Internal state for rate calculation
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.RWMutex // Protects concurrent access to metrics
	wg         sync.WaitGroup
	lastUpdate time.Time // Last time rates were calculated
	lastSent   int64     // Previous DataSent value for rate calculation
	lastRecvd  int64     // Previous DataRecvd value for rate calculation
}

func newMetrics(ctx context.Context) *metrics {
	ctx2, cancel := context.WithCancel(ctx)
	m := &metrics{
		ctx:    ctx2,
		cancel: cancel,
	}

	m.wg.Add(1)
	go m.loop()

	return m
}

func (m *metrics) updateRates() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if !m.lastUpdate.IsZero() {
		duration := now.Sub(m.lastUpdate).Seconds()
		if duration > 0 {
			m.DataSentRate = float32(m.DataSent-m.lastSent) / float32(duration)
			m.DataRecvdRate = float32(m.DataRecvd-m.lastRecvd) / float32(duration)
		}
	}

	m.lastUpdate = now
	m.lastSent = m.DataSent
	m.lastRecvd = m.DataRecvd
}

func (m *metrics) loop() {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.updateRates()
		}
	}
}

func (m *metrics) Close() error {
	if m.cancel != nil {
		m.cancel()

		m.wg.Wait()
	}

	return nil
}

func (m *metrics) addSent(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataSent += bytes
}

func (m *metrics) addRecvd(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataRecvd += bytes
}

func (m *metrics) GetStats() (sent, recvd int64, sentRate, recvdRate float32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.DataSent, m.DataRecvd, m.DataSentRate, m.DataRecvdRate
}

type LoopBack struct {
	bindPort *net.UDPConn // UDP socket for bidirectional communication
	remote   *net.UDPAddr // Remote endpoint address (auto-discovered or manually set)

	metrics *metrics // Real-time performance metrics

	mu sync.RWMutex // Protects concurrent access to connection state
}

func NewLoopBack(ctx context.Context, bindAddr string, options ...Option) (*LoopBack, error) {
	addr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve bind address: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
	}

	l := &LoopBack{
		bindPort: conn,
		metrics:  newMetrics(ctx),
	}

	for _, option := range options {
		if err := option(l); err != nil {
			return nil, fmt.Errorf("error while applying option; err: %v", err)
		}
	}

	fmt.Printf("LoopBack connected on %s\n", l.bindPort.LocalAddr().String())

	return l, nil
}

func (l *LoopBack) Consume(payload []byte) error {
	return l.write(payload)
}

func (l *LoopBack) write(payload []byte) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		fmt.Printf("bind port not yet set. Skipping message; no error")
		return nil
	}
	if l.remote == nil {
		fmt.Printf("remote port not yet discovered. Skipping message; no error")
		return nil
	}

	bytesWritten, err := l.bindPort.WriteToUDP(payload, l.remote)
	if err != nil {
		return fmt.Errorf("failed to write UDP message: %v", err)
	}

	if bytesWritten != len(payload) {
		return fmt.Errorf("written bytes (%d) != message length (%d)", bytesWritten, len(payload))
	}

	l.metrics.addSent(int64(bytesWritten))

	return nil
}

func (l *LoopBack) Generate() ([]byte, error) {
	data, err := l.read()
	if err != nil {
		return nil, fmt.Errorf("ERROR: loopback failed to generate; err: %w", err)
	}

	return data, nil
}

func (l *LoopBack) read() ([]byte, error) {
	// Set read timeout to allow periodic context checking
	if err := l.bindPort.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		fmt.Printf("Error while setting read deadline on UDP bind port: %v. Continuing...\n", err)
	}

	buff, nRead := l.readMessageFromUDPPort()
	if nRead > 0 && buff != nil {
		// Update metrics
		l.metrics.addRecvd(int64(nRead))

		return buff[:nRead], nil
	}

	// Return nil to indicate no data available (Reader should handle appropriately)
	return nil, nil
}

func (l *LoopBack) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	fmt.Println("Closing LoopBack...")

	// Close UDP connection
	var err error
	if l.bindPort != nil {
		err = l.bindPort.Close()
		l.bindPort = nil
	}

	fmt.Println("LoopBack closed")
	return err
}

func (l *LoopBack) readMessageFromUDPPort() ([]byte, int) {
	buffer := make([]byte, 1500) // Standard MTU size

	nRead, senderAddr, err := l.bindPort.ReadFromUDP(buffer)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, 0
		}
		fmt.Printf("Error while reading message from bind port: %v\n", err)
		return nil, 0
	}

	// Auto-discover remote address from first received packet
	l.mu.Lock()
	if l.remote == nil {
		l.remote = &net.UDPAddr{IP: senderAddr.IP, Port: senderAddr.Port}
		fmt.Printf("Auto-discovered remote address: %s\n", l.remote.String())
	}
	l.mu.Unlock()

	if senderAddr != nil && l.remote != nil && senderAddr.Port != l.remote.Port {
		fmt.Printf("Warning: expected port %d but got %d\n", l.remote.Port, senderAddr.Port)
	}

	return buffer, nRead
}

func (l *LoopBack) SetRemoteAddress(address string) error {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return fmt.Errorf("failed to resolve remote address: %v", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.remote = addr

	fmt.Printf("Remote address set to: %s\n", addr.String())
	return nil
}

func (l *LoopBack) GetMetrics() (sent, recvd int64, sentRate, recvdRate float32) {
	return l.metrics.GetStats()
}

func (l *LoopBack) GetLocalAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		return ""
	}
	return l.bindPort.LocalAddr().String()
}

func (l *LoopBack) GetRemoteAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.remote == nil {
		return ""
	}
	return l.remote.String()
}
