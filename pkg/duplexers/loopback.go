package duplexers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/metrics"
)

type LoopBack struct {
	bindPort *net.UDPConn // UDP socket for bidirectional communication
	remote   *net.UDPAddr // Remote endpoint address (auto-discovered or manually set)

	metrics *metrics.UnifiedMetrics // Real-time performance metrics

	mu sync.RWMutex // Protects concurrent access to connection state
}

func NewLoopBack(ctx context.Context, bindAddr string, options ...LoopBackOption) (*LoopBack, error) {
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
		metrics:  metrics.NewUnifiedMetrics(ctx, fmt.Sprintf("LoopBack-%s", addr.String()), 5, 3*time.Second),
	}

	l.metrics.SetState(metrics.ClientStateConnecting)

	for _, option := range options {
		if err := option(l); err != nil {
			l.metrics.AddErrors(err)
			return nil, fmt.Errorf("error while applying option; err: %v", err)
		}
	}

	l.metrics.SetState(metrics.ClientStateConnected)

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
		err := fmt.Errorf("bind port not yet set")
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ClientStateError)
		fmt.Printf("bind port not yet set. Skipping message; no error")
		return nil
	}
	if l.remote == nil {
		fmt.Printf("remote port not yet discovered. Skipping message; no error")
		return nil
	}

	bytesWritten, err := l.bindPort.WriteToUDP(payload, l.remote)
	if err != nil {
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ClientStateError)
		return fmt.Errorf("failed to write UDP message: %v", err)
	}

	if bytesWritten != len(payload) {
		err := fmt.Errorf("written bytes (%d) != message length (%d)", bytesWritten, len(payload))
		l.metrics.AddErrors(err)
		return err
	}

	// Update metrics
	l.metrics.IncrementBytesWritten(uint64(bytesWritten))
	l.metrics.IncrementPacketsWritten()
	l.metrics.SetLastWriteTime(time.Now())

	if l.metrics.GetState() != metrics.ClientStateConnected {
		l.metrics.SetState(metrics.ClientStateConnected)
	}

	return nil
}

func (l *LoopBack) Generate() ([]byte, error) {
	data, err := l.read()
	if err != nil {
		l.metrics.AddErrors(err)
		return nil, fmt.Errorf("ERROR: loopback failed to generate; err: %w", err)
	}

	return data, nil
}

func (l *LoopBack) read() ([]byte, error) {
	// Set read timeout to allow periodic context checking // TODO: ADD CONSISTENT TIMEOUT
	if err := l.bindPort.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		l.metrics.AddErrors(err)
		fmt.Printf("Error while setting read deadline on UDP bind port: %v. Continuing...\n", err)
	}

	buff, nRead := l.readMessageFromUDPPort()
	if nRead > 0 && buff != nil {
		// Update metrics
		l.metrics.IncrementBytesRead(uint64(nRead))
		l.metrics.IncrementPacketsRead()
		l.metrics.SetLastReadTime(time.Now())

		if l.metrics.GetState() != metrics.ClientStateConnected {
			l.metrics.SetState(metrics.ClientStateConnected)
		}

		return buff[:nRead], nil
	}

	// Return nil to indicate no data available (Reader should handle appropriately)
	return nil, nil
}

func (l *LoopBack) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	fmt.Println("Closing LoopBack...")

	l.metrics.SetState(metrics.ClientStateDisconnected)

	var err error
	if l.bindPort != nil {
		err = l.bindPort.Close()
		l.bindPort = nil
	}

	if l.metrics != nil {
		if metricsErr := l.metrics.Close(); metricsErr != nil {
			fmt.Printf("Error closing metrics: %v\n", metricsErr)
		}
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
			// Timeout is not really an error
			return nil, 0
		}
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ClientStateError)
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
		err := fmt.Errorf("expected port %d but got %d", l.remote.Port, senderAddr.Port)
		l.metrics.AddErrors(err)
		fmt.Printf("Warning: %v\n", err)
	}

	return buffer, nRead
}

func (l *LoopBack) SetRemoteAddress(address string) error {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		l.metrics.AddErrors(err)
		return fmt.Errorf("failed to resolve remote address: %v", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.remote = addr

	fmt.Printf("Remote address set to: %s\n", addr.String())
	return nil
}

func (l *LoopBack) GetMetrics() metrics.MetricsSnapshot {
	return l.metrics.GetSnapshot()
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
