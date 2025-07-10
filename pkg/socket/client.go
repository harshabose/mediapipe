// Package socket provides a robust, production-ready WebSocket client with
// comprehensive connection management, authentication handling, and integration
// with the universal media routing system.
//
// # Overview
//
// The socket package implements a WebSocket client designed for reliable,
// long-running connections in production environments. It provides automatic
// reconnection, JWT-based authentication with token refresh, comprehensive
// metrics tracking, and seamless integration with the media routing pipeline.
//
// # Key Features
//
//   - Automatic reconnection with exponential backoff
//   - JWT authentication with automatic token refresh
//   - Thread-safe operations and metrics
//   - Integration with universal media routing (CanGenerate/CanConsume)
//   - Connection health monitoring and staleness detection
//   - Comprehensive error tracking and diagnostics
//   - Graceful shutdown with proper resource cleanup
//
// # Basic Usage
//
//	config := socket.ClientConfig{
//	    Addr:     "wss://example.com",
//	    Port:     8080,
//	    AuthURL:  "https://auth.example.com",
//	    Username: "user",
//	    Password: "pass",
//	    MessageType: websocket.MessageBinary,
//	    ReadTimeout:  32768,  // 32KB, 0 = use max uint16
//	    WriteTimeout: 32768,  // 32KB, 0 = use max uint16
//	    MaxRetry:       5,
//	    ReconnectDelay: 5 * time.Second,
//	}
//
//	client := socket.NewClient(ctx, config)
//	client.Connect()
//	defer client.Close()
//
//	// Use as data source in media pipeline
//	reader := mediasource.NewIdentityAnyReader(client)
//	bufferedReader := mediasource.NewBufferedReader(ctx, reader, 100)
//
//	// Use as data sink in media pipeline
//	writer := mediapipe.NewIdentityAnyWriter(client)
//	bufferedWriter := mediapipe.NewBufferedWriter(ctx, writer, 100)
//
// # Connection Management
//
// The client automatically handles connection lifecycle:
//   - Initial connection with authentication
//   - Automatic reconnection on failures with exponential backoff
//   - JWT token refresh before expiration (5% buffer)
//   - Connection health monitoring with staleness detection
//   - Graceful shutdown with proper resource cleanup
//
// # Error Handling
//
// Errors are handled at multiple levels:
//   - Connection errors trigger automatic reconnection
//   - Authentication errors trigger full reconnection cycle
//   - Operation errors are tracked in metrics for diagnostics
//   - Recent errors are buffered for inspection and monitoring
//
// # Thread Safety
//
// All client operations are thread-safe and can be called concurrently:
//   - Generate() and Consume() can be called from multiple goroutines
//   - Metrics can be accessed safely during operations
//   - Connection state changes are atomic and consistent
//
// # Monitoring and Metrics
//
// The client provides comprehensive metrics for production monitoring:
//   - Connection state and transitions
//   - Packet and byte counters for throughput monitoring
//   - Timestamp tracking for staleness detection
//   - Recent error history for diagnostics
//   - All metrics are JSON-serializable for integration with monitoring systems

package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	mediapipe "github.com/harshabose/mediapipe"
)

// ClientState represents the current operational state of the client connection.
// These states provide visibility into the client's lifecycle and help
// with monitoring and debugging connection issues in production environments.
type ClientState int

const (
	// ClientStateDisconnected indicates the client is not connected to the server.
	// This is the initial state and the state after intentional disconnection or
	// when maximum retry attempts have been exhausted.
	ClientStateDisconnected ClientState = iota

	// ClientStateConnecting indicates the client is actively attempting to establish
	// a connection to the server. This includes DNS resolution, TCP/WebSocket connection,
	// and authentication phases.
	ClientStateConnecting

	// ClientStateConnected indicates the client is successfully connected and actively
	// communicating with the server. This is the desired operational state.
	ClientStateConnected

	// ClientStateError indicates the client has encountered an error condition.
	// The client will typically attempt automatic recovery unless configured otherwise.
	ClientStateError
)

// String returns a human-readable representation of the ClientState.
func (cs ClientState) String() string {
	switch cs {
	case ClientStateDisconnected:
		return "Disconnected"
	case ClientStateConnecting:
		return "Connecting"
	case ClientStateConnected:
		return "Connected"
	case ClientStateError:
		return "Error"
	default:
		return "Unknown"
	}
}

// BufferedErrors is a thread-safe, fixed-size circular buffer for storing error messages.
// It automatically removes the oldest errors when the buffer reaches capacity,
// ensuring bounded memory usage while maintaining recent error history for diagnostics.
//
// This is particularly useful for long-running connections where you want to
// track recent issues without unbounded memory growth.
type BufferedErrors struct {
	maxSize int
	errors  []string
	mux     sync.RWMutex
}

// NewBufferedErrors creates a new BufferedErrors with the specified maximum size.
// The buffer will hold at most maxSize error messages, removing the oldest
// when new errors are added beyond capacity.
//
// Parameters:
//   - maxSize: Maximum number of error messages to retain (must be > 0)
//
// Returns a new BufferedErrors instance ready for use.
func NewBufferedErrors(maxSize int) *BufferedErrors {
	return &BufferedErrors{
		maxSize: maxSize,
		errors:  make([]string, 0, maxSize),
	}
}

// Add adds a new error to the buffer, removing the oldest one if the buffer is full.
// This operation is thread-safe and can be called concurrently from multiple goroutines.
//
// The error is converted to its string representation for storage, preserving
// the error message while avoiding potential memory leaks from error value retention.
//
// Parameters:
//   - err: The error to add to the buffer (must not be nil)
func (be *BufferedErrors) Add(err error) {
	be.mux.Lock()
	defer be.mux.Unlock()

	if len(be.errors) >= be.maxSize {
		// Remove the oldest error (first element)
		be.errors = be.errors[1:]
	}

	// Add the new error
	be.errors = append(be.errors, err.Error())
}

// MarshalJSON implements the json.Marshaler interface, allowing BufferedErrors
// to be serialized as a JSON array of error message strings.
//
// This enables easy integration with monitoring systems, logging frameworks,
// and diagnostic tools that consume JSON-formatted data.
//
// Returns:
//   - []byte: JSON representation of the error buffer as an array of strings
//   - error: Any error that occurred during JSON marshaling
func (be *BufferedErrors) MarshalJSON() ([]byte, error) {
	be.mux.RLock()
	defer be.mux.RUnlock()

	return json.Marshal(be.errors)
}

// ClientMetrics tracks comprehensive statistics about the client's operation.
// These metrics provide real-time visibility into client performance and are
// essential for production monitoring, debugging, and capacity planning.
//
// All metrics operations are thread-safe and can be safely accessed from
// multiple goroutines concurrently. The metrics are designed to be lightweight
// and suitable for high-frequency updates without significant performance impact.
//
// Metrics include:
//   - Connection state and operational status
//   - Throughput counters (packets and bytes)
//   - Timing information for staleness detection
//   - Recent error history for diagnostics
//
// The metrics are JSON-serializable for easy integration with monitoring
// systems and can be exported to time-series databases for trend analysis.
type ClientMetrics struct {
	state          ClientState     // Current operational state
	packetsRead    uint64          // Total number of packets successfully read
	packetsWritten uint64          // Total number of packets successfully sent
	bytesRead      uint64          // Total number of bytes successfully read
	bytesWritten   uint64          // Total number of bytes successfully written
	lastWriteTime  time.Time       // Timestamp of the most recent successful write
	lastReadTime   time.Time       // Timestamp of the most recent successful read
	recentErrors   *BufferedErrors // Circular buffer of recent errors
	mux            sync.RWMutex    // Protects concurrent access to metrics
}

// SetState atomically updates the client's operational state.
// State changes are thread-safe and immediately visible to all readers.
// This method is typically called by the connection management system.
//
// Parameters:
//   - state: The new ClientState to set
func (m *ClientMetrics) SetState(state ClientState) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.state = state
}

// GetState returns the current operational state of the client.
// This method is thread-safe and can be called concurrently with state updates.
// It's commonly used by operation methods to validate connection status before proceeding.
//
// Returns:
//   - ClientState: The current state of the client connection
func (m *ClientMetrics) GetState() ClientState {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.state
}

// IncrementPacketsWritten atomically increments the packets written counter.
// This method is thread-safe and should be called after each successful
// packet transmission to maintain accurate throughput statistics.
func (m *ClientMetrics) IncrementPacketsWritten() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.packetsWritten++
}

// IncrementPacketsRead atomically increments the packets read counter.
// This method is thread-safe and should be called after each successful
// packet reception to maintain accurate throughput statistics.
func (m *ClientMetrics) IncrementPacketsRead() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.packetsRead++
}

// IncrementBytesWritten atomically increments the bytes written counter.
// This method is thread-safe and should be called after each successful
// data transmission with the number of bytes sent.
//
// Parameters:
//   - len: Number of bytes that were successfully written
func (m *ClientMetrics) IncrementBytesWritten(len uint64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.bytesWritten += len
}

// IncrementBytesRead atomically increments the bytes read counter.
// This method is thread-safe and should be called after each successful
// data reception with the number of bytes received.
//
// Parameters:
//   - len: Number of bytes that were successfully read
func (m *ClientMetrics) IncrementBytesRead(len uint64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.bytesRead += len
}

// GetLastWriteTime returns the timestamp of the most recent successful write operation.
// This method is thread-safe and is commonly used by connection monitoring
// systems to detect stale or inactive connections.
//
// Returns:
//   - time.Time: Timestamp of the last successful write (zero time if no writes yet)
func (m *ClientMetrics) GetLastWriteTime() time.Time {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.lastWriteTime
}

// GetLastReadTime returns the timestamp of the most recent successful read operation.
// This method is thread-safe and is commonly used by connection monitoring
// systems to detect stale or inactive connections.
//
// Returns:
//   - time.Time: Timestamp of the last successful read (zero time if no reads yet)
func (m *ClientMetrics) GetLastReadTime() time.Time {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.lastReadTime
}

// SetLastWriteTime updates the timestamp of the most recent write operation.
// This method is thread-safe and should be called after each successful
// data transmission to maintain accurate timing information.
//
// Parameters:
//   - t: Timestamp of the write operation
func (m *ClientMetrics) SetLastWriteTime(t time.Time) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.lastWriteTime = t
}

// SetLastReadTime updates the timestamp of the most recent read operation.
// This method is thread-safe and should be called after each successful
// data reception to maintain accurate timing information.
//
// Parameters:
//   - t: Timestamp of the read operation
func (m *ClientMetrics) SetLastReadTime(t time.Time) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.lastReadTime = t
}

// AddErrors records one or more errors for diagnostic purposes.
// This method is thread-safe and filters out nil errors automatically.
// Errors are stored in a circular buffer to prevent unbounded memory growth.
//
// The errors are also typically logged elsewhere, but storing them here
// provides programmatic access for monitoring and diagnostic tools.
//
// Parameters:
//   - errs: Variable number of errors to record (nil errors are ignored)
func (m *ClientMetrics) AddErrors(errs ...error) {
	if len(errs) == 0 {
		return
	}

	m.mux.Lock()
	defer m.mux.Unlock()

	for _, err := range errs {
		if err == nil {
			continue
		}
		m.recentErrors.Add(err)
	}
}

// GetMetrics returns a snapshot of all current metrics in a JSON-serialisable format.
// thread-safe.
//
// Returns:
//   - map[string]interface{}: Complete metrics snapshot with keys:
//   - "state": Current connection state as string
//   - "packetsRead": Total packets successfully read
//   - "packetsWritten": Total packets successfully written
//   - "bytesRead": Total bytes successfully read
//   - "bytesWritten": Total bytes successfully written
//   - "lastReadTime": ISO 8601 timestamp of last read (if any)
//   - "lastWriteTime": ISO 8601 timestamp of last write (if any)
//   - "recentErrors": Array of recent error messages
func (m *ClientMetrics) GetMetrics() map[string]interface{} {
	m.mux.RLock()
	defer m.mux.RUnlock()

	metrics := map[string]interface{}{
		"state":          m.state.String(),
		"packetsRead":    m.packetsRead,
		"packetsWritten": m.packetsWritten,
		"bytesRead":      m.bytesRead,
		"bytesWritten":   m.bytesWritten,
		"recentErrors":   m.recentErrors,
	}

	// Include timestamps only if they're set (non-zero)
	if !m.lastReadTime.IsZero() {
		metrics["lastReadTime"] = m.lastReadTime.Format(time.RFC3339)
	}
	if !m.lastWriteTime.IsZero() {
		metrics["lastWriteTime"] = m.lastWriteTime.Format(time.RFC3339)
	}

	return metrics
}

// ClientConfig contains all configuration parameters for the WebSocket client.
// This includes connection details, authentication credentials, io timeouts,
// and retry behavior.
//
// Note: AccessToken is transmitted via query parameter for maximum client compatibility,
// though this is less secure than header-based authentication. Ensure HTTPS is used
// in production environments to protect credentials in transit.
type ClientConfig struct {
	// Connection settings
	Addr string // WebSocket server address (e.g., "wss://example.com")
	Port uint16 // WebSocket server port
	Path string

	// Authentication settings // NOT IMPLEMENTED FOR NOW
	// AuthURL  string // Authentication service URL for login
	// Username string // Username for authentication
	// Password string // Password for authentication

	// WebSocket protocol settings
	MessageType websocket.MessageType // Type of WebSocket messages (Binary/Text)

	// Buffer configuration
	ReadTimeout  time.Duration // Maximum read buffer size (0 = use max uint16 ~64KB)
	WriteTimeout time.Duration // Maximum write buffer size (0 = use max uint16 ~64KB)

	// Connection management
	KeepConnecting bool          // Whether to maintain a persistent connection
	MaxRetry       uint8         // Maximum reconnection attempts (0 = unlimited)
	ReconnectDelay time.Duration // Initial delay between reconnection attempts
}

func (c *ClientConfig) updateDelay() {
	newDelay := time.Duration(float64(c.ReconnectDelay) * 1.5)
	if newDelay > 30*time.Second {
		newDelay = 30 * time.Second
	}
	c.ReconnectDelay = newDelay
}

func (c *ClientConfig) shouldRetry(attempt uint8) bool {
	if !c.KeepConnecting {
		fmt.Println("Not retrying as not configured to reconnect")
		return false
	}

	if c.MaxRetry == 0 {
		fmt.Printf("No retries configured, stopping connection attempts\n")
		return false
	}

	if c.MaxRetry > 0 && attempt >= c.MaxRetry {
		fmt.Printf("Maximum retry attempts (%d) reached, stopping\n", c.MaxRetry)
		return false
	}

	return true
}

func (c *ClientConfig) waitForRetry(ctx context.Context, attempt uint8) bool {
	fmt.Printf("Retrying Websocket connection in %v (attempt %d)\n", c.ReconnectDelay, attempt+1)

	select {
	case <-ctx.Done():
		fmt.Printf("Websocket connection manager stopping during retry delay\n")
		return false
	case <-time.After(c.ReconnectDelay):
		return true
	}
}

// Client represents a WebSocket client with automatic reconnection, authentication,
// and integration with the universal media routing system.
//
// The client implements both CanGenerate[[]byte] and CanConsume[[]byte] interfaces,
// making it suitable for use as both a data source and sink in media pipelines.
//
// Key features:
//   - Automatic connection management with exponential backoff retry
//   - JWT-based authentication with automatic token refresh
//   - Thread-safe operations suitable for concurrent use
//   - Comprehensive metrics and error tracking
//   - Integration with media routing pipeline
//   - Graceful shutdown with proper resource cleanup
//
// The client is designed for long-running, production environments where
// connection reliability and observability are critical requirements.
type Client struct {
	// auth *auth.TokenManager // Authentication client for login/token management

	config ClientConfig // Client configuration parameters

	reader mediapipe.CanGenerate[[]byte] // WebSocket reader adapter for media pipeline
	writer mediapipe.CanConsume[[]byte]  // WebSocket writer adapter for media pipeline

	metrics *ClientMetrics // Operational metrics and statistics

	// Concurrency and lifecycle management
	once      sync.Once          // Ensures Close() is idempotent
	ctx       context.Context    // Client context for cancellation
	cancel    context.CancelFunc // Function to cancel client operations
	mux       sync.RWMutex       // Protects reader/writer access
	wg        sync.WaitGroup     // Synchronizes background goroutines
	reconnect chan struct{}      // Channel for triggering reconnection
}

// NewClient creates a new WebSocket client with the specified configuration.
// The client is initialized but not connected - call Connect() to establish
// the connection.
//
// The client automatically sets up:
//   - Authentication client for token management
//   - Metrics tracking with error buffering
//   - Reconnection signaling channel
//   - Context cancellation for graceful shutdown
//
// Parameters:
//   - ctx: Parent context for client lifecycle (cancellation propagates to client)
//   - config: Complete client configuration including connection and auth details
//
// Returns:
//   - *Client: Configured client ready for connection
//
// Example:
//
//	config := ClientConf
//	config := ClientConfig{
//	    Addr: "wss://api.example.com",
//	    Port: 443,
//	    AuthURL: "https://auth.example.com",
//	    Username: "user@example.com",
//	    Password: "secure-password",
//	    MessageType: websocket.MessageBinary,
//	    MaxRetry: 5,
//	    ReconnectDelay: 5 * time.Second,
//	}
//	client := NewClient(ctx, config)
func NewClient(ctx context.Context, config ClientConfig) *Client {
	ctx2, cancel := context.WithCancel(ctx)

	c := &Client{
		// auth:      auth.NewTokenManager(ctx2, tokenManagerConfig),
		config:    config,
		metrics:   &ClientMetrics{recentErrors: NewBufferedErrors(10)},
		reconnect: make(chan struct{}, 1),
		ctx:       ctx2,
		cancel:    cancel,
	}

	return c
}

func (c *Client) Connect() {
	c.wg.Add(1)
	go c.connect()

	fmt.Printf("attempting connection to %s:%d%s\n", c.config.Addr, c.config.Port, c.config.Path)

}

// Connect initiates the WebSocket connection in a background goroutine.
// This method returns immediately while connection establishment proceeds
// asynchronously with automatic retry logic.
//
//  1. Authentication with the configured auth service
//  2. WebSocket connection establishment
//  3. ReaderWriter/writer setup for media pipeline integration
//  4. Connection health monitoring
//  5. Automatic reconnection on failures
//  5. Automatic reconnection on failures
//
// Connection state changes are reflected in the metrics and can be monitored
// via GetMetrics(). The client will continue attempting connections until
// successful, maximum retries are reached, or the context is cancelled.
//
// This method is safe to call multiple times - subsequent calls are ignored
// if a connection process is already running.
func (c *Client) ConnectAndWait() <-chan struct{} {
	c.Connect()
	return c.ctx.Done()
}

func (c *Client) Wait() <-chan struct{} {
	return c.ctx.Done()
}

// connect implements the main connection management loop with exponential backoff retry.
// This method runs in a background goroutine and handles the complete connection
// lifecycle including authentication, WebSocket establishment, and monitoring.
func (c *Client) connect() {
	defer c.wg.Done()
	defer c.metrics.SetState(ClientStateDisconnected)

	var attempt uint8 = 0

	for {
		select {
		case <-c.ctx.Done():
			fmt.Println("socket connection manager stopping due to context cancellation")
			return
		default:
			c.metrics.SetState(ClientStateConnecting)
			fmt.Printf("Attempting to connect to websocket server: %s\n", c.config.Addr)

			_, err := c.attemptConnection()
			if err != nil {
				c.metrics.SetState(ClientStateError)
				c.metrics.AddErrors(err)
				fmt.Printf("Websocket connection failed: %v\n", err)

				if !c.config.shouldRetry(attempt) {
					return
				}

				if !c.config.waitForRetry(c.ctx, attempt) {
					return
				}

				c.config.updateDelay()

				attempt++
				continue
			}

			// Connection successful
			c.metrics.SetState(ClientStateConnected)
			fmt.Printf("Websocket connection established to: %s\n", c.config.Addr)

			fmt.Println("Starting connection monitor...")
			c.monitorConnection()

			// Connection lost - reset for retry
			fmt.Printf("Connection lost; preparing to retry (attempt %d)\n", attempt+1)

			if !c.config.shouldRetry(attempt) {
				return
			}

			if !c.config.waitForRetry(c.ctx, attempt) {
				return
			}

			c.config.updateDelay()
			attempt++
		}
	}
}

// monitorConnection runs connection health monitoring in a loop.
// It checks for connection staleness and handles reconnection signals.
func (c *Client) monitorConnection() {
	writesTicker := time.NewTicker(5 * time.Second)
	defer writesTicker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.reconnect:
			fmt.Println("Triggered websocket reconnection due to some error")
			return
		case <-writesTicker.C:
			if time.Since(c.metrics.GetLastWriteTime()) > 60*time.Second || time.Since(c.metrics.GetLastReadTime()) > 60*time.Second {
				fmt.Printf("socket connection appears stale (no writes and/or read for 60s)\n")
			}
		}
	}
}

// attemptConnection performs a single connection attempt including authentication
// and WebSocket establishment. Returns an error if any step fails.
func (c *Client) attemptConnection() (*websocket.Conn, error) {
	// if err := c.performLogin(); err != nil {
	// 	return nil, err
	// }

	// token, ok := c.auth.GetCurrentToken()
	// if !ok {
	// 	return errors.New("token is not valid")
	// }

	// conn, _, err := websocket.Dial(c.ctx, fmt.Sprintf("%s:%d?token=%s", c.config.Addr, c.config.Port, token), nil)
	// if err != nil {
	// 	return err
	// }

	url := fmt.Sprintf("%s:%d%s", c.config.Addr, c.config.Port, c.config.Path)
	conn, _, err := websocket.Dial(c.ctx, url, nil)
	if err != nil {
		return nil, err
	}

	fmt.Println("dail success")

	if err := c.setReaderWriter(conn); err != nil {
		return nil, err
	}

	return conn, err
}

// setReaderWriter atomically updates the client's reader and writer.
// This method is thread-safe and ensures consistent reader/writer state.
func (c *Client) setReaderWriter(conn *websocket.Conn) error {
	rw := NewSocketReaderWriter(c.ctx, conn, c.config.MessageType, c.config.ReadTimeout, c.config.WriteTimeout)

	c.mux.Lock()
	defer c.mux.Unlock()

	c.reader = rw
	c.writer = rw

	return nil
}

// Generate implements the CanGenerate[[]byte] interface, allowing the client
// to act as a data source in the universal media routing system.
//
// This method reads data from the WebSocket connection and returns it as bytes.
// It includes proper error handling, state validation, and metrics tracking.
//
//  1. Validate the client is in connected state
//  2. Read data from the WebSocket connection
//  3. Update metrics for successful reads
//  4. Trigger reconnection on read failures
//  4. Trigger reconnection on read failures
//
// Returns:
//   - []byte: Data read from the WebSocket connection
//   - error: Any error that occurred during reading
//
// Errors are automatically tracked in metrics and may trigger reconnection
// attempts if they indicate connection issues.
func (c *Client) Generate() ([]byte, error) {
	select {
	case <-c.ctx.Done():
		err := errors.New("context is cancelled; cannot generate now")
		c.metrics.AddErrors(err)
		return nil, err
	default:
		if c.metrics.GetState() != ClientStateConnected {
			err := fmt.Errorf("cannot transmit data: client state is %s, expected %s (connected)", c.metrics.GetState().String(), ClientStateConnected.String())
			c.metrics.AddErrors(err)
			fmt.Println(err.Error())
			return nil, nil
		}

		c.mux.RLock()
		defer c.mux.RUnlock()

		if c.reader == nil {
			err := errors.New("reader not ready yet")
			c.metrics.AddErrors(err)
			fmt.Println(err.Error())
			return nil, nil
		}

		data, err := c.reader.Generate()
		if err != nil {
			c.metrics.SetState(ClientStateError)
			c.metrics.AddErrors(err)

			select {
			case c.reconnect <- struct{}{}:
			default:
				// Channel full, reconnection already pending
			}

			return nil, fmt.Errorf("failed to read data from websocket server: %w", err)
		}

		c.metrics.IncrementPacketsRead()
		c.metrics.IncrementBytesRead(uint64(len(data)))
		c.metrics.SetLastReadTime(time.Now())

		return data, nil
	}
}

// Consume implements the CanConsume[[]byte] interface, allowing the client
// to act as a data sink in the universal media routing system.
//
// This method writes data to the WebSocket connection with proper error handling,
// state validation, and metrics tracking.
//
//  1. Validate the client is in connected state
//  2. Write data to the WebSocket connection
//  3. Update metrics for successful writes
//  4. Trigger reconnection on write failures
//  4. Trigger reconnection on write failures
//
// Parameters:
//   - data: Byte slice to write to the WebSocket connection
//
// Returns:
//   - error: Any error that occurred during writing
//
// Errors are automatically tracked in metrics and may trigger reconnection
// attempts if they indicate connection issues.
func (c *Client) Consume(data []byte) error {
	select {
	case <-c.ctx.Done():
		return errors.New("context cancelled; cannot consume now")
	default:
		if c.metrics.GetState() != ClientStateConnected {
			err := fmt.Errorf("cannot transmit data: client state is %d, expected %d (connected)", c.metrics.GetState(), ClientStateConnected)
			c.metrics.AddErrors(err)
			fmt.Println(err.Error())
			return nil
		}

		c.mux.RLock()
		defer c.mux.RUnlock()

		if c.writer == nil {
			err := errors.New("writer not ready yet")
			c.metrics.AddErrors(err)
			fmt.Println(err.Error())
			return nil
		}

		if err := c.writer.Consume(data); err != nil {
			c.metrics.SetState(ClientStateError)
			c.metrics.AddErrors(err)

			select {
			case c.reconnect <- struct{}{}:
			default:
				// Channel full, reconnection already pending
			}

			return fmt.Errorf("failed to write data to websocket server: %w", err)
		}

		c.metrics.IncrementPacketsWritten()
		c.metrics.IncrementBytesWritten(uint64(len(data)))
		c.metrics.SetLastWriteTime(time.Now())

		return nil
	}
}

// GetMetrics returns a complete snapshot of the client's operational metrics.
// This method is thread-safe and provides point-in-time visibility into
// client performance for monitoring and diagnostic purposes.
//
// The returned metrics include connection state, throughput counters,
// timing information, and recent error history. All data is formatted
// for easy integration with monitoring systems and JSON serialization.
//
// Returns:
//   - map[string]interface{}: Complete metrics snapshot suitable for monitoring
//
// Example:
//
//	metrics := client.GetMetric
//	metrics := client.GetMetrics()
//	fmt.Printf("Connection state: %s\n", metrics["state"])
//	fmt.Printf("Bytes written: %d\n", metrics["bytesWritten"])
func (c *Client) GetMetrics() map[string]interface{} {
	return c.metrics.GetMetrics()
}

// performLogin authenticates with the configured auth service and obtains
// JWT tokens for WebSocket authentication.
// func (c *Client) performLogin() error {
// 	// ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
// 	// defer cancel()
// 	//
// 	// _, err := c.auth.Login(ctx, c.config.Username, c.config.Password)
// 	// if err != nil {
// 	// 	return err
// 	// }
//
// 	return nil
// }

// Close gracefully shuts down the client and cleans up all resources.
// This method is safe to call multiple times and from multiple goroutines.
//
//  1. Cancels the client context to stop all background operations
//  2. Waits for all background goroutines to complete
//  3. Ensures proper resource cleanup
//  3. Ensures proper resource cleanup
//
// After Close() returns, the client cannot be reused and a new client
// must be created for subsequent connections.
//
// Returns:
//   - error: Always returns nil in current implementation
//
// Example:
//
//	client := NewClient(ctx, conf
//	client := NewClient(ctx, config)
//	client.Connect()
//	defer client.Close() // Ensures cleanup on function exit
//
//	// Or explicit cleanup with error handling
//	if err := client.Close(); err != nil {
//	    log.Printf("Error closing client: %v", err)
//	}
func (c *Client) Close() error {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}

		c.wg.Wait()
	})

	return nil
}
