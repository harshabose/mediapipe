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

type LocalUDP struct {
	bind   *udp
	remote *udp

	metrics *metrics.UnifiedMetrics
	ctx     context.Context
	cancel  context.CancelFunc

	mux  sync.RWMutex
	pool sync.Pool
}

func NewLoopBack(ctx context.Context, options ...LoopBackOption) (*LocalUDP, error) {
	ctx2, cancel2 := context.WithCancel(ctx)
	l := &LocalUDP{
		bind:   &udp{ip: "127.0.0.1"},
		remote: &udp{ip: "127.0.0.1"},
		ctx:    ctx2,
		cancel: cancel2,
	}

	l.pool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 1500) // Standard MTU size
			return &buf
		},
	}

	for _, option := range options {
		if err := option(l); err != nil {
			cancel2()
			return nil, fmt.Errorf("error while applying option; err: %v", err)
		}
	}

	if err := l.bind.connect(); err != nil {
		cancel2()
		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
	}

	if l.remote.port != 0 {
		if err := l.remote.findAddr(); err != nil {
			cancel2()
			l.bind.conn.Close()
			return nil, fmt.Errorf("failed to find remote udp addr: %v", err)
		}
	}

	l.metrics = metrics.NewUnifiedMetrics(ctx, fmt.Sprintf("LocalUDP-%s", l.bind.conn.LocalAddr().String()), 5, 3*time.Second)
	l.metrics.SetState(metrics.ConnectedState)

	fmt.Printf("LocalUDP connected on %s\n", l.bind.conn.LocalAddr().String())
	return l, nil
}

func NewLocalNet(ctx context.Context, ip string, options ...LoopBackOption) (*LocalUDP, error) {
	ctx2, cancel2 := context.WithCancel(ctx)
	l := &LocalUDP{
		bind:   &udp{ip: ip},
		remote: &udp{ip: ip},
		ctx:    ctx2,
		cancel: cancel2,
	}

	// Initialize buffer pool
	l.pool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 1500) // Standard MTU size
			return &buf
		},
	}

	for _, option := range options {
		if err := option(l); err != nil {
			cancel2()
			return nil, fmt.Errorf("error while applying option; err: %v", err)
		}
	}

	if err := l.bind.connect(); err != nil {
		cancel2()
		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
	}

	if l.remote.port != 0 {
		if err := l.remote.findAddr(); err != nil {
			cancel2()
			l.bind.conn.Close()
			return nil, fmt.Errorf("failed to find remote udp addr: %v", err)
		}
	}

	l.metrics = metrics.NewUnifiedMetrics(ctx, fmt.Sprintf("LocalUDP-%s", l.bind.conn.LocalAddr().String()), 5, 3*time.Second)
	l.metrics.SetState(metrics.ConnectedState)

	fmt.Printf("LocalUDP connected on %s\n", l.bind.conn.LocalAddr().String())
	return l, nil
}

func (l *LocalUDP) Consume(payload []byte) error {
	return l.write(payload)
}

func (l *LocalUDP) write(payload []byte) error {
	select {
	case <-l.ctx.Done():
		return fmt.Errorf("context cancelled: %w", l.ctx.Err())
	default:
	}

	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		err := fmt.Errorf("bind port not yet set")
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ErrorState)
		return err
	}
	if l.remote.addr == nil {
		err := fmt.Errorf("remote port not yet discovered")
		l.metrics.AddErrors(err)
		return err
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

	l.metrics.IncrementBytesWritten(uint64(bytesWritten))
	l.metrics.IncrementPacketsWritten()
	l.metrics.SetLastWriteTime(time.Now())

	if l.metrics.GetState() != metrics.ConnectedState {
		l.metrics.SetState(metrics.ConnectedState)
	}

	return nil
}

func (l *LocalUDP) Generate() ([]byte, error) {
	data, err := l.read()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		l.metrics.AddErrors(err)
		return nil, fmt.Errorf("ERROR: udp failed to generate; err: %w", err)
	}

	return data, nil
}

func (l *LocalUDP) read() ([]byte, error) {
	select {
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	default:
	}

	// Set read timeout to allow periodic context checking
	if err := l.bind.conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		l.metrics.AddErrors(err)
		fmt.Printf("Error while setting read deadline on UDP bind port: %v. Continuing...\n", err)
	}

	buff, nRead, err := l.readMessageFromUDPPort()
	if err != nil {
		return nil, err
	}

	if nRead > 0 && buff != nil {
		l.metrics.IncrementBytesRead(uint64(nRead))
		l.metrics.IncrementPacketsRead()
		l.metrics.SetLastReadTime(time.Now())

		if l.metrics.GetState() != metrics.ConnectedState {
			l.metrics.SetState(metrics.ConnectedState)
		}

		data := make([]byte, nRead)
		copy(data, (*buff)[:nRead])

		l.pool.Put(buff)

		return data, nil
	}

	if buff != nil {
		l.pool.Put(buff)
	}

	// Returning nil to indicate no data available (Reader handle appropriately)
	return nil, nil
}

func (l *LocalUDP) Close() error {
	l.mux.Lock()
	defer l.mux.Unlock()

	fmt.Println("Closing LocalUDP...")

	if l.cancel != nil {
		l.cancel()
	}

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

	fmt.Println("LocalUDP closed")
	return err
}

func (l *LocalUDP) readMessageFromUDPPort() (*[]byte, int, error) {
	bufPtr := l.pool.Get().(*[]byte)
	buffer := *bufPtr

	nRead, senderAddr, err := l.bind.conn.ReadFromUDP(buffer)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			// Timeout is not really an error - return buffer to pool
			l.pool.Put(bufPtr)
			return nil, 0, nil
		}
		l.metrics.AddErrors(err)
		l.metrics.SetState(metrics.ErrorState)
		fmt.Printf("Error while reading message from bind port: %v\n", err)
		// Return buffer to pool on error
		l.pool.Put(bufPtr)
		return nil, 0, err
	}

	// Auto-discover remote address from first received packet
	l.mux.Lock()
	var remoteAddrPort int
	if l.remote.addr == nil {
		l.remote.addr = &net.UDPAddr{IP: senderAddr.IP, Port: senderAddr.Port}
		l.remote.ip = senderAddr.IP.String()
		l.remote.port = senderAddr.Port
		fmt.Printf("Auto-discovered remote address: %s\n", l.remote.addr.String())
		remoteAddrPort = l.remote.addr.Port
	} else {
		remoteAddrPort = l.remote.addr.Port
	}
	l.mux.Unlock()

	// Check port mismatch while NOT holding the lock
	if senderAddr != nil && senderAddr.Port != remoteAddrPort {
		err := fmt.Errorf("expected port %d but got %d", remoteAddrPort, senderAddr.Port)
		l.metrics.AddErrors(err)
		fmt.Printf("Warning: %v\n", err)
	}

	return bufPtr, nRead, nil
}

func (l *LocalUDP) SetRemoteAddress(address string) error {
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

func (l *LocalUDP) GetMetrics() metrics.Snapshot {
	return l.metrics.GetSnapshot()
}

func (l *LocalUDP) GetLocalAddress() string {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		return ""
	}
	return l.bind.conn.LocalAddr().String()
}

func (l *LocalUDP) GetRemoteAddress() string {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.remote.addr == nil {
		return ""
	}
	return l.remote.addr.String()
}

func (l *LocalUDP) GetBindPort() uint16 {
	l.mux.RLock()
	defer l.mux.RUnlock()

	if l.bind.conn == nil {
		return 0
	}
	return uint16(l.bind.conn.LocalAddr().(*net.UDPAddr).Port)
}
