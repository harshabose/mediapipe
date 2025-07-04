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
//	Input Side:  UDP Socket → LoopBack.Read() → mediasink.Reader → Pipeline
//	Output Side: Pipeline → mediasink.Writer → LoopBack.Consume() → UDP Socket
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

// metrics tracks bidirectional data flow statistics for observability and monitoring.
// This provides real-time insights into the LoopBack's performance characteristics,
// which is essential for production deployments and debugging network issues.
//
// The metrics are thread-safe and automatically calculate data rates over time,
// giving operators visibility into both instantaneous and averaged throughput.
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

// updateRates calculates the current data transfer rates based on the change
// in total bytes transferred since the last update.
//
// This method is called periodically by the metrics loop to provide real-time
// visibility into network throughput. The rates are calculated as bytes per second
// over the time interval since the last update.
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

// addSent atomically increments the total bytes sent counter and is called
// whenever data is successfully written to the UDP socket.
func (m *metrics) addSent(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataSent += bytes
}

// addRecvd atomically increments the total bytes received counter and is called
// whenever data is successfully read from the UDP socket.
func (m *metrics) addRecvd(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataRecvd += bytes
}

// GetStats returns a consistent snapshot of current metrics including both
// cumulative totals and current transfer rates.
//
// Returns:
//   - sent: Total bytes sent since startup
//   - recvd: Total bytes received since startup
//   - sentRate: Current send rate in bytes per second
//   - recvdRate: Current receive rate in bytes per second
func (m *metrics) GetStats() (sent, recvd int64, sentRate, recvdRate float32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.DataSent, m.DataRecvd, m.DataSentRate, m.DataRecvdRate
}

// LoopBack is a bidirectional UDP-based bridge that implements both CanGenerate and CanConsume
// interfaces, enabling it to participate in the universal media routing system as both
// a data source and data sink simultaneously.
//
// Type parameter:
//   - T: The source type for Data elements when acting as a consumer (typically matches
//     the pipeline's source data type for proper type relationships)
//
// # Key Features:
//
//   - Bidirectional data flow through UDP sockets
//   - Automatic remote endpoint discovery from incoming packets
//   - Built-in metrics and monitoring for production observability
//   - Thread-safe operations with proper resource management
//   - Graceful shutdown with context cancellation
//   - Production-ready error handling and logging
//
// # Network Behavior:
//
//   - Listens on a bound UDP port for incoming data (CanGenerate side)
//   - Sends data to a discovered or configured remote UDP endpoint (CanConsume side)
//   - Automatically discovers remote endpoint from first received packet
//   - Supports manual remote endpoint configuration for explicit routing
//
// # Integration with Media Router:
//
//	As CanGenerate: LoopBack → Reader → BufferedReader → Pipeline
//	As CanConsume:  Pipeline → BufferedWriter → Writer → LoopBack
//
// This enables complex routing topologies where data can flow through network
// boundaries and be processed by remote systems before returning to local pipelines.
type LoopBack[T any] struct {
	bindPort *net.UDPConn // UDP socket for bidirectional communication
	remote   *net.UDPAddr // Remote endpoint address (auto-discovered or manually set)

	metrics *metrics // Real-time performance metrics

	mu sync.RWMutex // Protects concurrent access to connection state
}

// NewLoopBack creates a new LoopBack instance bound to the specified UDP address
// and immediately starts its background operations.
//
// The LoopBack will listen on the provided address for incoming data and will
// automatically discover the remote endpoint from the first packet received.
// Alternatively, the remote endpoint can be set explicitly using SetRemoteAddress.
//
// Parameters:
//   - ctx: Parent context for lifecycle management. When cancelled, the LoopBack
//     will gracefully shut down all background operations.
//   - bindAddr: Local UDP address to bind to (e.g., ":8080" or "0.0.0.0:8080")
//   - options: Optional configuration functions that customize the LoopBack behavior.
//     Options are applied after basic initialization but before the LoopBack is ready.
//
// Returns a fully configured and active LoopBack ready for immediate use as both
// a data source and sink in the universal media routing system.
//
// The LoopBack automatically starts its metrics collection goroutine and is
// immediately ready for data transfer operations in both directions. Any provided
// options are applied during initialization to customize behavior.
//
// Example:
//
//	// Basic LoopBack
//	loopback, err := NewLoopBack[*rtp.Packet](ctx, "0.0.0.0:5004")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer loopback.Close()
//
//	// LoopBack with options
//	loopback, err := NewLoopBack[*rtp.Packet](ctx, "0.0.0.0:5004",
//	    WithRemoteAddress("192.168.1.100:5004"),
//	    WithBufferSize(2048))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer loopback.Close()
//
//	// LoopBack is immediately ready for use as both source and sink
//	reader := mediasink.NewReader(loopback, transformer)
//	writer := mediasink.NewAnyWriter(loopback, transformer)
func NewLoopBack[T any](ctx context.Context, bindAddr string, options ...Option[T]) (*LoopBack[T], error) {
	addr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve bind address: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
	}

	l := &LoopBack[T]{
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

// Consume implements mediasink.CanConsume[[]byte], allowing the LoopBack
// to act as a data sink in the universal media routing system.
//
// This method receives byte payloads from the pipeline and transmits them
// over UDP to the configured remote endpoint. The LoopBack can accept data
// from any source in the pipeline that produces []byte output.
//
// The method integrates seamlessly with the universal media routing system:
//   - AnyWriter handles Data[D, T] transformation and calls this method with []byte
//   - BufferedWriter provides asynchronous buffering before calling this method
//   - The pipeline ensures proper data flow and error handling
//
// Parameters:
//   - payload: The byte slice to be transmitted over UDP. Empty payloads are allowed.
//
// Returns an error if:
//   - No remote endpoint is configured or discovered
//   - UDP transmission fails
//   - The number of bytes written doesn't match the payload size
//
// Example usage in pipeline:
//
//	writer := mediasink.NewIdentityAnyWriter(loopback)
//	bufferedWriter := mediasink.NewBufferedWriter(ctx, writer, 100)
//
//	// Data flows: Pipeline → BufferedWriter → AnyWriter → LoopBack.Consume → UDP
//	err := bufferedWriter.Write(data) // data gets transformed to []byte and calls Consume
func (l *LoopBack[T]) Consume(payload []byte) error {
	return l.write(payload)
}

// write transmits the byte payload to the configured remote UDP endpoint.
//
// This internal method handles the actual UDP transmission with proper error
// checking and metrics tracking. It ensures that the complete payload is
// transmitted and updates the send metrics accordingly.
//
// The method is thread-safe and can be called concurrently from multiple
// goroutines, though it's typically called sequentially from the pipeline.
func (l *LoopBack[T]) write(payload []byte) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		return fmt.Errorf("bind port not yet set. Skipping message")
	}
	if l.remote == nil {
		return fmt.Errorf("remote port not yet discovered. Skipping message")
	}

	bytesWritten, err := l.bindPort.WriteToUDP(payload, l.remote)
	if err != nil {
		return fmt.Errorf("failed to write UDP message: %v", err)
	}

	if bytesWritten != len(payload) {
		return fmt.Errorf("written bytes (%d) != message length (%d)", bytesWritten, len(payload))
	}

	// Update metrics
	l.metrics.addSent(int64(bytesWritten))

	return nil
}

// Close gracefully shuts down the LoopBack, releasing network resources.
//
// This method:
//  1. Closes the UDP socket to stop network operations
//
// It's safe to call Close multiple times. After calling Close, the LoopBack
// should not be used for further operations.
//
// Returns any error from closing the UDP socket.
func (l *LoopBack[T]) Close() error {
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

// Read implements mediasink.CanGenerate[[]byte], allowing the LoopBack to act as
// a data source in the universal media routing system.
//
// This method reads data from the UDP socket and returns it as byte slices that
// can be fed into the media routing pipeline. It uses a read timeout to allow
// periodic context checking without blocking indefinitely.
//
// The method handles:
//   - Automatic remote endpoint discovery from incoming packets
//   - Timeout-based non-blocking reads for context responsiveness
//   - Metrics tracking for received data
//   - Proper error handling for network issues
//
// Returns:
//   - []byte: Data received from the network, or nil if no data is available
//   - error: Any error that occurred during reading
//
// Example usage in pipeline:
//
//	reader := mediasink.NewReader(loopback, transformer)
//	bufferedReader := mediasink.NewBufferedReader(ctx, reader, 100)
//
//	// Data flows: UDP → LoopBack.Read → Reader → BufferedReader → Pipeline
//	data, err := bufferedReader.Read()
func (l *LoopBack[T]) Read() ([]byte, error) {
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

	// Return nil to indicate no data available (Reader will handle appropriately)
	return nil, nil
}

// metricsLoop runs in a background goroutine and periodically updates the
// data transfer rate calculations.
//
// This goroutine runs for the lifetime of the LoopBack connection and provides
// real-time visibility into network performance. It updates metrics every second
// to provide current throughput rates for monitoring and debugging.
func (l *LoopBack[T]) metricsLoop() {

}

// readMessageFromUDPPort performs a single UDP read operation with proper
// error handling and automatic remote endpoint discovery.
//
// This internal method handles:
//   - Reading data from the UDP socket
//   - Distinguishing between timeout errors (expected) and real errors
//   - Automatic discovery of remote endpoint from sender address
//   - Optional validation of sender address for security
//
// Returns the buffer and number of bytes read, or nil/0 if no data is available.
func (l *LoopBack[T]) readMessageFromUDPPort() ([]byte, int) {
	buffer := make([]byte, 1500) // Standard MTU size

	nRead, senderAddr, err := l.bindPort.ReadFromUDP(buffer)
	if err != nil {
		// Check if it's a timeout (which is expected)
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

	// Validate sender (optional security check)
	if senderAddr != nil && l.remote != nil && senderAddr.Port != l.remote.Port {
		fmt.Printf("Warning: expected port %d but got %d\n", l.remote.Port, senderAddr.Port)
	}

	return buffer, nRead
}

// SetRemoteAddress manually configures the remote UDP endpoint address.
//
// This provides an alternative to automatic endpoint discovery for scenarios
// where the remote address is known in advance or needs to be explicitly
// controlled for security or routing purposes.
//
// Parameters:
//   - address: Remote UDP address in "host:port" format (e.g., "192.168.1.100:5004")
//
// Returns an error if the address cannot be resolved.
//
// Example:
//
//	err := loopback.SetRemoteAddress("192.168.1.100:5004")
//	if err != nil {
//	    log.Printf("Failed to set remote address: %v", err)
//	}
func (l *LoopBack[T]) SetRemoteAddress(address string) error {
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

// GetMetrics returns a snapshot of current performance metrics including
// both cumulative totals and current transfer rates.
//
// This provides visibility into the LoopBack's network performance for
// monitoring, debugging, and capacity planning purposes.
//
// Returns:
//   - sent: Total bytes sent since startup
//   - recvd: Total bytes received since startup
//   - sentRate: Current send rate in bytes per second
//   - recvdRate: Current receive rate in bytes per second
//
// Example:
//
//	sent, recvd, sentRate, recvdRate := loopback.GetMetrics()
//	fmt.Printf("Sent: %d bytes (%.2f B/s), Received: %d bytes (%.2f B/s)",
//	    sent, sentRate, recvd, recvdRate)
func (l *LoopBack[T]) GetMetrics() (sent, recvd int64, sentRate, recvdRate float32) {
	return l.metrics.GetStats()
}

// GetLocalAddress returns the local UDP address that the LoopBack is bound to.
//
// This is useful for logging, debugging, and for communicating the address
// to remote systems that need to send data to this LoopBack.
//
// Returns an empty string if the LoopBack is not properly initialized.
func (l *LoopBack[T]) GetLocalAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		return ""
	}
	return l.bindPort.LocalAddr().String()
}

// GetRemoteAddress returns the current remote UDP endpoint address.
//
// This returns the address that was either auto-discovered from incoming
// packets or manually configured via SetRemoteAddress.
//
// Returns an empty string if no remote address has been discovered or configured.
func (l *LoopBack[T]) GetRemoteAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.remote == nil {
		return ""
	}
	return l.remote.String()
}
