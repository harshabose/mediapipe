package duplexers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/metrics"
)

type udp struct {
	conn *net.UDPConn
	addr *net.UDPAddr
	ip   string
	port int
}

func (u *udp) connect() error {
	if err := u.findAddr(); err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", u.addr)
	if err != nil {
		return err
	}

	u.conn = conn
	return nil
}

func (u *udp) findAddr() error {
	addr, err := net.ResolveUDPAddr("udp", u.ip+":"+strconv.Itoa(u.port))
	if err != nil {
		return err
	}

	u.addr = addr
	return nil
}

type LoopBack struct {
	bind   *udp
	remote *udp

	metrics *metrics.UnifiedMetrics

	mux sync.RWMutex
}

func NewLoopBack(ctx context.Context, options ...LoopBackOption) (*LoopBack, error) {
	l := &LoopBack{
		bind:   &udp{ip: "127.0.0.1"},
		remote: &udp{ip: "127.0.0.1"},
	}

	for _, option := range options {
		if err := option(l); err != nil {
			return nil, fmt.Errorf("error while applying option; err: %v", err)
		}
	}

	if err := l.bind.connect(); err != nil {
		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
	}

	if l.remote.port != 0 {
		if err := l.remote.findAddr(); err != nil {
			return nil, fmt.Errorf("failed to find remote udp addr: %v", err)
		}
	}

	l.metrics = metrics.NewUnifiedMetrics(ctx, fmt.Sprintf("LoopBack-%s", l.bind.conn.LocalAddr().String()), 5, 3*time.Second)
	l.metrics.SetState(metrics.ConnectedState)

	fmt.Printf("LoopBack connected on %s\n", l.bind.conn.LocalAddr().String())
	return l, nil
}

func (l *LoopBack) Consume(payload []byte) error {
	return l.write(payload)
}

func (l *LoopBack) write(payload []byte) error {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		err := fmt.Errorf("bind port not yet set")
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ErrorState)
		fmt.Printf("bind port not yet set. Skipping message; no error")
		return nil
	}
	if l.remote.addr == nil {
		fmt.Printf("remote port not yet discovered. Skipping message; no error")
		return nil
	}

	bytesWritten, err := l.bind.conn.WriteToUDP(payload, l.remote.addr)
	if err != nil {
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ErrorState)
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

	if l.metrics.GetState() != metrics.ConnectedState {
		l.metrics.SetState(metrics.ConnectedState)
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
	// Set read timeout to allow periodic context checking
	if err := l.bind.conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		l.metrics.AddErrors(err)
		fmt.Printf("Error while setting read deadline on UDP bind port: %v. Continuing...\n", err)
	}

	buff, nRead := l.readMessageFromUDPPort()
	if nRead > 0 && buff != nil {
		// Update metrics
		l.metrics.IncrementBytesRead(uint64(nRead))
		l.metrics.IncrementPacketsRead()
		l.metrics.SetLastReadTime(time.Now())

		if l.metrics.GetState() != metrics.ConnectedState {
			l.metrics.SetState(metrics.ConnectedState)
		}

		return buff[:nRead], nil
	}

	// Return nil to indicate no data available (Reader should handle appropriately)
	return nil, nil
}

func (l *LoopBack) Close() error {
	l.mux.Lock()
	defer l.mux.Unlock()

	fmt.Println("Closing LoopBack...")

	l.metrics.SetState(metrics.DisconnectedState)

	var err error
	if l.bind.conn != nil {
		err = l.bind.conn.Close()
		l.bind.conn = nil
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

	nRead, senderAddr, err := l.bind.conn.ReadFromUDP(buffer)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			// Timeout is not really an error
			return nil, 0
		}
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ErrorState)
		fmt.Printf("Error while reading message from bind port: %v\n", err)
		return nil, 0
	}

	// Auto-discover remote address from first received packet
	l.mux.Lock()
	if l.remote.addr == nil {
		l.remote.addr = &net.UDPAddr{IP: senderAddr.IP, Port: senderAddr.Port}
		l.remote.ip = senderAddr.IP.String()
		l.remote.port = senderAddr.Port
		fmt.Printf("Auto-discovered remote address: %s\n", l.remote.addr.String())
	}
	l.mux.Unlock()

	if senderAddr != nil && l.remote.addr != nil && senderAddr.Port != l.remote.addr.Port {
		err := fmt.Errorf("expected port %d but got %d", l.remote.addr.Port, senderAddr.Port)
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

	l.mux.Lock()
	defer l.mux.Unlock()
	l.remote.addr = addr
	l.remote.ip = addr.IP.String()
	l.remote.port = addr.Port

	fmt.Printf("Remote address set to: %s\n", addr.String())
	return nil
}

func (l *LoopBack) GetMetrics() metrics.Snapshot {
	return l.metrics.GetSnapshot()
}

func (l *LoopBack) GetLocalAddress() string {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		return ""
	}
	return l.bind.conn.LocalAddr().String()
}

func (l *LoopBack) GetRemoteAddress() string {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.remote.addr == nil {
		return ""
	}
	return l.remote.addr.String()
}

func (l *LoopBack) GetBindPort() uint16 {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		return 0
	}
	return uint16(l.bind.conn.LocalAddr().(*net.UDPAddr).Port)
}
