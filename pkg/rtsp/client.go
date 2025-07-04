// Package rtsp provides a production-ready RTSP client implementation that integrates
// seamlessly with the universal media routing system as a CanConsume[*rtp.Packet] destination.
//
// # RTSP Client for Media Streaming
//
// This package implements a robust RTSP client designed for reliable media streaming
// to RTSP servers. It handles the complexities of RTSP protocol negotiation,
// connection management, and automatic recovery from network failures.
//
// # Key Features
//
//   - Automatic connection management with intelligent retry logic
//   - Exponential backoff for reconnection attempts
//   - Real-time health monitoring and stale connection detection
//   - Comprehensive ClientMetrics collection for production monitoring
//   - Thread-safe operations with proper resource management
//   - Graceful shutdown with context-based lifecycle management
//   - Flexible configuration with sensible defaults
//   - Options pattern for extensible initialisation
//
// # Integration with Universal Media Router
//
// The RTSP client implements CanConsume[*rtp.Packet], making it a perfect destination
// for the universal media routing system:
//
//	WebRTC Track → Reader → BufferedWriter → RTSP Client → RTSP Serve
//	File Source → Reader → BufferedWriter → RTSP Client → RTSP Serve
//	Any Source → Universal Pipeline → RTSP Client → RTSP Serve
//
// This enables any media source to be streamed to RTSP servers with the same
// clean interface and automatic buffering capabilities.
//
// # Production-Ready Reliability
//
// The client is designed for 24/7 operation with automatic recovery from:
//   - Network connectivity issues
//   - RTSP server restarts or downtime
//   - Temporary connection drops
//   - Protocol-level errors
//   - Resource exhaustion scenarios
//
// # Real-World Use Cases
//
//	Security Camera Systems: Stream camera feeds to RTSP servers
//	Live Broadcasting: Forward live streams to RTSP distribution servers
//	Media Transcoding: Send processed media to RTSP endpoints
//	Multi-Protocol Distribution: Convert WebRTC/UDP streams to RTSP
//	Surveillance Systems: Relay video streams to recording servers
//
// # Example Usage
//
//	// Basic RTSP streaming setup
//	config := rtsp.DefaultClientConfig()
//	config.ServerAddr = "rtsp.example.com"
//	config.ServerPort = 8554
//	config.StreamPath = "live/stream1"
//
//	client, err := rtsp.NewClient(ctx, config, mediaDescription,
//	    rtsp.WithH264Media(1920, 1080, 30))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
//	// Use with universal media router
//	writer := mediasink.NewAnyWriter(client, transformer)
//	buffered := mediasink.NewBufferedWriter(ctx, writer, 100)
//
//	// Stream any RTP source to RTSP server
//	buffered.Write(rtpData)
package rtsp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

// ClientState represents the current operational state of the RTSP client.
// These states provide visibility into the client's lifecycle and help
// with monitoring and debugging connection issues.
type ClientState int

const (
	// ClientStateDisconnected indicates the client is not connected to the RTSP server.
	// This is the initial state and the state after intentional disconnection or
	// when maximum retry attempts have been exhausted.
	ClientStateDisconnected ClientState = iota

	// ClientStateConnecting indicates the client is actively attempting to establish
	// a connection to the RTSP server. This includes DNS resolution, TCP connection,
	// and RTSP protocol negotiation phases.
	ClientStateConnecting

	// ClientStateRecording indicates the client is successfully connected and actively
	// streaming RTP packets to the RTSP server. This is the desired operational state.
	ClientStateRecording

	// ClientStateError indicates the client has encountered an error condition.
	// The client will typically attempt automatic recovery unless configured otherwise.
	ClientStateError
)

// ClientConfig defines the configuration parameters for the RTSP client.
// All timeouts and intervals can be customised for different network conditions
// and server requirements. The configuration provides fine-grained control over
// connection behaviour, retry logic, and protocol parameters.
type ClientConfig struct {
	ServerAddr        string        // RTSP server hostname or IP address
	ServerPort        int           // RTSP server port (typically 8554)
	StreamPath        string        // RTSP stream path (e.g., "live/stream1")
	ReadTimeout       time.Duration // Timeout for reading responses from server
	WriteTimeout      time.Duration // Timeout for writing packets to server
	DialTimeout       time.Duration // Timeout for establishing TCP connection
	ReconnectAttempts int           // Maximum retry attempts (-1 for infinite)
	ReconnectDelay    time.Duration // Initial delay between reconnection attempts
	UserAgent         string        // User-Agent header for RTSP requests
	Username          string
	Password          string
}

// DefaultClientConfig returns a ClientConfig with production-ready default values.
//
// The defaults are tuned for typical RTSP server deployments and provide a good
// balance between responsiveness and stability. The configuration can be customised
// after creation for specific deployment requirements.
//
// Default values:
//   - ServerAddr: "localhost" (should be changed for production)
//   - ServerPort: 8554 (standard RTSP port)
//   - StreamPath: "stream" (generic stream path)
//   - ReadTimeout: 10 seconds
//   - WriteTimeout: 10 seconds
//   - DialTimeout: 10 seconds
//   - ReServeAttempts: 3 attempts
//   - ReconnectDelay: 2 seconds initial delay
//   - UserAgent: "GoRTSP-Host/1.0"
//
// Example:
//
//	config := rtsp.DefaultClientConfig()
//	config.ServerAddr = "production.rtsp.server.com"
//	config.ReServeAttempts = 10  // More retries for production
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		ServerAddr:        "localhost",
		ServerPort:        8554,
		StreamPath:        "stream",
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		DialTimeout:       10 * time.Second,
		ReconnectAttempts: 3,
		ReconnectDelay:    2 * time.Second,
		UserAgent:         "GoRTSP-Host/1.0",
	}
}

// ClientMetrics tracks comprehensive statistics about the RTSP client's operation.
// These ClientMetrics provide real-time visibility into client performance and are
// essential for production monitoring, debugging, and capacity planning.
//
// All ClientMetrics operations are thread-safe and can be safely accessed from
// multiple goroutines concurrently.
type ClientMetrics struct {
	state          ClientState  // Current operational state
	packetsWritten uint64       // Total number of RTP packets successfully sent
	bytesWritten   uint64       // Total number of bytes successfully transmitted
	lastWriteTime  time.Time    // Timestamp of the most recent successful write
	lastError      error        // Most recent error encountered (if any)
	mux            sync.RWMutex // Protects concurrent access to ClientMetrics
}

// SetState atomically updates the client's operational state.
// State changes are logged by the connection management system for monitoring.
func (m *ClientMetrics) SetState(state ClientState) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.state = state
}

// GetState returns the current operational state of the client.
// This is used by the Consume method to reject writes when not in recording state.
func (m *ClientMetrics) GetState() ClientState {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.state
}

// IncrementPacketsWritten atomically increments the packet counter.
// Called after each successful RTP packet transmission.
func (m *ClientMetrics) IncrementPacketsWritten() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.packetsWritten++
}

// IncrementBytesWritten atomically increments the byte counter.
// Called after each successful RTP packet transmission with the payload size.
func (m *ClientMetrics) IncrementBytesWritten(len uint64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.bytesWritten += len
}

// GetLastWriteTime returns the timestamp of the most recent successful write.
// Used by connection monitoring to detect stale connections.
func (m *ClientMetrics) GetLastWriteTime() time.Time {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.lastWriteTime
}

// SetLastWriteTime updates the timestamp of the most recent write operation.
// Called after each successful packet transmission.
func (m *ClientMetrics) SetLastWriteTime(t time.Time) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.lastWriteTime = t
}

// SetLastError records the most recent error for diagnostic purposes.
// Errors are also logged but stored here for programmatic access.
func (m *ClientMetrics) SetLastError(err error) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.lastError = err
}

// Client represents a production-ready RTSP client that can stream RTP packets
// to RTSP servers with automatic connection management and error recovery.
//
// The client implements CanConsume[*rtp.Packet], making it compatible with
// the universal media routing system. It handles all aspects of RTSP protocol
// communication, connection lifecycle, and error recovery automatically.
//
// # Key Capabilities:
//
//   - Automatic RTSP protocol negotiation (ANNOUNCE, SETUP, RECORD)
//   - Intelligent reconnection with exponential backoff
//   - Real-time connection health monitoring
//   - Comprehensive ClientMetrics collection
//   - Thread-safe operation for concurrent access
//   - Graceful shutdown with proper resource clean-up
//
// # Lifecycle Management:
//
//	Creation → Background Connection → Recording State → Clean-up
//
// The client automatically maintains its connection in a background goroutine,
// handling reconnections transparently while continuing to accept RTP packets
// for transmission when connected.
type Client struct {
	serverAddr string // Configured server address
	streamPath string // Configured stream path
	rtspURL    string // Complete RTSP URL (constructed from config)

	// RTSP protocol components
	client      *gortsplib.Client    // Underlying gortsplib client instance
	description *description.Session // SDP session description for the stream

	// Lifecycle management
	mux    sync.RWMutex       // Protects access to client connection state
	ctx    context.Context    // Context for graceful shutdown coordination
	cancel context.CancelFunc // Cancellation function for stopping operations
	wg     sync.WaitGroup     // WaitGroup for goroutine coordination

	// Configuration and control
	config        *ClientConfig // Client configuration parameters
	reconnectChan chan struct{} // Channel for signalling reconnection needs

	// Observability
	metrics *ClientMetrics // Real-time operational metrics
}

// NewClient creates a new RTSP client with the specified configuration and
// immediately starts background connection management.
//
// The client will attempt to connect to the RTSP server in a background goroutine,
// handling reconnections automatically. The client is ready to accept RTP packets
// via Consume() immediately, though packets will be queued or dropped if not
// currently connected.
//
// Parameters:
//   - ctx: Parent context for lifecycle management. When cancelled, the client
//     will gracefully shut down all operations.
//   - config: Client configuration. If nil, DefaultClientConfig() is used.
//   - des: SDP session description. If nil, an empty session is created.
//     Media descriptions should be added via options or AppendRTSPMediaDescription.
//   - options: Optional configuration functions that customise client behaviour.
//
// Returns a fully configured client with background connection management active.
// The client requires at least one media description to be valid.
//
// Example:
//
//	config := rtsp.DefaultClientConfig()
//	config.ServerAddr = "rtsp.example.com"
//
//	client, err := rtsp.NewClient(ctx, config, nil,
//	    rtsp.WithH264Media(1920, 1080, 30))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
//	// Use with universal media router
//	writer := mediasink.NewAnyWriter(client, transformer)
//	buffered := mediasink.NewBufferedWriter(ctx, writer, 100)
func NewClient(ctx context.Context, config *ClientConfig, des *description.Session, options ...ClientOption) (*Client, error) {
	if config == nil {
		config = DefaultClientConfig()
	}

	if des == nil {
		des = &description.Session{}
	}

	ctx2, cancel := context.WithCancel(ctx)

	client := &Client{
		serverAddr:    config.ServerAddr,
		streamPath:    config.StreamPath,
		rtspURL:       fmt.Sprintf("rtsp://%s:%s@%s:%d/%s", config.Username, config.Password, config.ServerAddr, config.ServerPort, config.StreamPath),
		description:   des,
		config:        config,
		reconnectChan: make(chan struct{}, 1),
		ctx:           ctx2,
		cancel:        cancel,
		metrics:       &ClientMetrics{},
	}

	// Apply configuration options
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	// Validate configuration after options are applied
	if len(client.description.Medias) == 0 {
		return nil, errors.New("no media descriptions configured - use options to add media")
	}

	fmt.Printf("RTSP client configured for URL: %s\n", client.rtspURL)

	// Serve background connection management
	client.wg.Add(1)
	go client.connect()

	return client, nil
}

// connect manages the client's connection lifecycle in a background goroutine.
//
// This method implements sophisticated reconnection logic with exponential backoff,
// ensuring reliable operation even in unstable network conditions. It handles:
//   - Initial connection establishment
//   - Automatic retry with configurable attempts
//   - Exponential backoff with maximum delay capping
//   - Connection health monitoring
//   - Graceful shutdown coordination
//
// The method runs for the lifetime of the client until context cancellation
// or maximum retry attempts are exhausted.
func (c *Client) connect() {
	defer c.wg.Done()

	attempt := 0
	currentDelay := c.config.ReconnectDelay
	maxAttempts := c.config.ReconnectAttempts

	for {
		// Check for shutdown signal
		select {
		case <-c.ctx.Done():
			fmt.Printf("RTSP connection manager stopping due to context cancellation\n")
			c.metrics.SetState(ClientStateDisconnected)
			return
		default:
		}

		// Attempt to establish connection
		c.metrics.SetState(ClientStateConnecting)
		fmt.Printf("Attempting to connect to RTSP server: %s\n", c.rtspURL)

		if err := c.attemptConnection(); err != nil {
			c.metrics.SetState(ClientStateError)
			c.metrics.SetLastError(err)
			fmt.Printf("RTSP connection failed: %v\n", err)

			// Check retry policy
			if maxAttempts == 0 {
				fmt.Printf("No retries configured, stopping connection attempts\n")
				c.metrics.SetState(ClientStateDisconnected)
				return
			}

			if maxAttempts > 0 && attempt >= maxAttempts {
				fmt.Printf("Maximum retry attempts (%d) reached, stopping\n", maxAttempts)
				c.metrics.SetState(ClientStateDisconnected)
				return
			}

			// Wait before retry with exponential backoff
			fmt.Printf("Retrying RTSP connection in %v (attempt %d)\n", currentDelay, attempt+1)
			select {
			case <-c.ctx.Done():
				fmt.Printf("RTSP connection manager stopping during retry delay\n")
				c.metrics.SetState(ClientStateDisconnected)
				return
			case <-time.After(currentDelay):
				// Continue to next attempt
			}

			// Apply exponential backoff with 30-second maximum
			currentDelay = time.Duration(float64(currentDelay) * 1.5)
			if currentDelay > 30*time.Second {
				currentDelay = 30 * time.Second
			}

			attempt++
			continue
		}

		// Connection established successfully
		c.metrics.SetState(ClientStateRecording)
		fmt.Printf("RTSP connection established, now recording to: %s\n", c.rtspURL)
		currentDelay = c.config.ReconnectDelay // Reset backoff delay
		attempt = 0                            // Reset attempt counter

		// Monitor connection health
		c.monitorConnection()

		// Connection monitoring returned - check why
		select {
		case <-c.ctx.Done():
			fmt.Printf("RTSP connection manager stopping - context cancelled\n")
			c.metrics.SetState(ClientStateDisconnected)
			return
		default:
			// Connection was lost, attempt reconnection
			fmt.Printf("RTSP connection lost, attempting to reconnect...\n")
			c.metrics.SetState(ClientStateConnecting)
		}
	}
}

// attemptConnection performs a single connection attempt to the RTSP server.
//
// This method handles the complete RTSP connection sequence:
//  1. Creates a new gortsplib client instance
//  2. Parses and validates the RTSP URL
//  3. Performs RTSP protocol negotiation (ANNOUNCE, SETUP, RECORD)
//  4. Updates ClientMetrics and state on success
//
// Returns an error if any step of the connection process fails.
func (c *Client) attemptConnection() error {
	c.mux.Lock()
	defer c.mux.Unlock()

	// Create fresh client instance for each connection attempt
	c.client = &gortsplib.Client{
		ReadTimeout:  c.config.ReadTimeout,
		WriteTimeout: c.config.WriteTimeout,
		UserAgent:    c.config.UserAgent,
	}

	// Perform RTSP protocol negotiation
	if err := c.client.StartRecording(c.rtspURL, c.description); err != nil {
		return fmt.Errorf("RTSP protocol negotiation failed: %w", err)
	}

	// Update ClientMetrics on successful connection
	c.metrics.SetState(ClientStateRecording)
	c.metrics.SetLastWriteTime(time.Now())

	return nil
}

// monitorConnection performs ongoing health monitoring of the RTSP connection.
//
// This method runs in the connection management goroutine and provides:
//   - Periodic connection health checks
//   - Stale connection detection based on write-activity
//   - Responsive shutdown on context cancellation
//   - Reconnection triggering on connection issues
//
// The method returns when the connection should be re-established or when
// the client is shutting down.
func (c *Client) monitorConnection() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			// Graceful shutdown requested
			return
		case <-c.reconnectChan:
			// Reconnection explicitly requested (usually due to write error)
			return
		case <-ticker.C:
			// Periodic health check
			if time.Since(c.metrics.GetLastWriteTime()) > 60*time.Second {
				fmt.Printf("RTSP connection appears stale (no writes for 60s)\n")
				// Log warning but continue - write errors will trigger reconnection
			}

			// Additional health checks could be implemented here
			// Currently, write errors are the primary connection failure detection
		}
	}
}

// Consume implements mediasink.CanConsume[*rtp.Packet], allowing the RTSP client
// to act as a destination in the universal media routing system.
//
// This method receives RTP packets from the media pipeline and transmits them
// to the configured RTSP server. It handles payload type validation and correction
// to ensure compatibility with the server's expected format.
//
// The method includes comprehensive error handling and automatic reconnection
// triggering when transmission failures occur.
//
// Parameters:
//   - packet: RTP packet to transmit. Nil packets are safely ignored.
//
// Returns an error if:
//   - The client is not in recording state
//   - No media formats are configured
//   - The underlying RTSP client is not initialized
//   - RTSP packet transmission fails
//
// On transmission errors, the method automatically triggers a reconnection
// attempt in the background while returning the error to the caller.
//
// Example usage in pipeline:
//
//	writer := mediasink.NewAnyWriter(rtspClient, transformer)
//	buffered := mediasink.NewBufferedWriter(ctx, writer, 100)
//
//	// Data flows: Pipeline → BufferedWriter → Writer → RTSP Client → RTSP Serve
//	err := buffered.Write(rtpData)
func (c *Client) Consume(packet *rtp.Packet) error {
	if packet == nil {
		// Gracefully handle nil packets (common in some pipeline scenarios)
		return nil
	}

	// Verify client is in an operational state
	state := c.metrics.GetState()
	if state != ClientStateRecording {
		return fmt.Errorf("cannot transmit packet: client state is %d, expected %d (recording)",
			state, ClientStateRecording)
	}

	c.mux.RLock()
	defer c.mux.RUnlock()

	// Ensure the client is properly initialised
	if c.client == nil {
		return errors.New("RTSP client not initialized")
	}

	// Validate media configuration
	if len(c.description.Medias) == 0 || len(c.description.Medias[0].Formats) == 0 {
		return errors.New("no media formats configured")
	}

	// Validate and correct payload type if necessary
	expectedPayloadType := c.description.Medias[0].Formats[0].PayloadType()
	if packet.PayloadType != expectedPayloadType {
		fmt.Printf("Correcting RTP payload type: expected %d, got %d\n",
			expectedPayloadType, packet.PayloadType)
		packet.PayloadType = expectedPayloadType
	}

	// Transmit the packet to the RTSP server
	if err := c.client.WritePacketRTP(c.description.Medias[0], packet); err != nil {
		c.metrics.SetState(ClientStateError)
		c.metrics.SetLastError(err)

		// Trigger reconnection attempt (non-blocking)
		select {
		case c.reconnectChan <- struct{}{}:
			fmt.Printf("Triggered RTSP reconnection due to write error\n")
		default:
			// Channel full, reconnection already pending
		}

		return fmt.Errorf("failed to write RTP packet to RTSP server: %w", err)
	}

	// Update success ClientMetrics
	c.metrics.IncrementPacketsWritten()
	c.metrics.IncrementBytesWritten(uint64(len(packet.Payload)))
	c.metrics.SetLastWriteTime(time.Now())

	return nil
}

// AppendRTSPMediaDescription adds a media description to the client's SDP session.
//
// This method allows dynamic addition of media streams to the RTSP session.
// It should typically be called before the client attempts its first connection,
// or during client initialisation via options.
//
// Parameters:
//   - media: Media description to add to the session
//
// The method is thread-safe and can be called concurrently with other operations.
//
// Example:
//
//	h264Media := &description.Media{
//	    Type: description.MediaTypeVideo,
//	    Formats: []format.Format{&format.H264{
//	        PayloadTyp: 96,
//	        PacketizationMode: 1,
//	    }},
//	}
//	client.AppendRTSPMediaDescription(h264Media)
func (c *Client) AppendRTSPMediaDescription(media *description.Media) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.description.Medias = append(c.description.Medias, media)
	fmt.Printf("Added media description to RTSP session (total: %d)\n", len(c.description.Medias))
}

// Close gracefully shuts down the RTSP client, stopping all background operations
// and releasing resources.
//
// This method:
//  1. Cancels the context to signal shutdown to all goroutines
//  2. Closes the underlying RTSP client connection
//  3. Waits for all background goroutines to complete
//  4. Updates the client state to Disconnected
//
// It's safe to call Close multiple times. After calling Close, the client
// should not be used for further operations.
//
// The method blocks until all clean-up is complete, ensuring proper resource
// release and no goroutine leaks.
func (c *Client) Close() error {
	fmt.Printf("Shutting down RTSP client for %s\n", c.rtspURL)

	// Signal shutdown to all goroutines
	if c.cancel != nil {
		c.cancel()
	}

	// Close RTSP connection
	c.mux.Lock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.mux.Unlock()

	// Wait for all goroutines to complete
	c.wg.Wait()

	// Update final state
	c.metrics.SetState(ClientStateDisconnected)
	fmt.Printf("RTSP client shutdown complete\n")

	return nil
}
