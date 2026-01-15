package consumers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"github.com/harshabose/tools/pkg/metrics"
)

type RTSPClientConfig struct {
	ServerAddr        string        `json:"server_addr"`        // RTSP server hostname or IP address
	ServerPort        int           `json:"server_port"`        // RTSP server port (typically 8554)
	StreamPath        string        `json:"stream_path"`        // RTSP stream path (e.g., "live/stream1")
	ReadTimeout       time.Duration `json:"read_timeout"`       // Timeout for reading responses from server
	WriteTimeout      time.Duration `json:"write_timeout"`      // Timeout for writing packets to server
	DialTimeout       time.Duration `json:"dial_timeout"`       // Timeout for establishing TCP connection
	ReconnectAttempts int           `json:"reconnect_attempts"` // Maximum retry attempts (-1 for infinite)
	ReconnectDelay    time.Duration `json:"reconnect_delay"`    // Initial delay between reconnection attempts
	UserAgent         string        `json:"user_agent"`         // User-Agent header for RTSP requests
	// Username string `json:"-"`
	// Password string `json:"-"`
	WriteQueueSize int `json:"write_queue_size"`
}

func DefaultRTSPClientConfig() *RTSPClientConfig {
	return &RTSPClientConfig{
		ServerAddr:        "localhost",
		ServerPort:        8554,
		StreamPath:        "stream",
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		DialTimeout:       10 * time.Second,
		ReconnectAttempts: 3,
		ReconnectDelay:    2 * time.Second,
		UserAgent:         "GoRTSP-Host/1.0",
		WriteQueueSize:    4096,
	}
}

type RTSPClient struct {
	serverAddr string // Configured server address
	streamPath string // Configured stream path
	rtspURL    string // Complete RTSP URL (constructed from config)

	// RTSP protocol components
	client      *gortsplib.Client    // Underlying gortsplib client instance
	description *description.Session // SDP process description for the stream

	// Lifecycle management
	mux    sync.RWMutex       // Protects access to client connection state
	ctx    context.Context    // Context for graceful shutdown coordination
	cancel context.CancelFunc // Cancellation function for stopping operations
	wg     sync.WaitGroup     // WaitGroup for goroutine coordination

	// Configuration and control
	config        *RTSPClientConfig // RTSPClient configuration parameters
	reconnectChan chan struct{}     // Channel for signalling reconnection needs

	// Observability
	metrics *metrics.UnifiedMetrics // Real-time operational metrics
}

func NewRTSPClient(ctx context.Context, config *RTSPClientConfig, des *description.Session, options ...RTSPClientOption) (*RTSPClient, error) {
	if config == nil {
		config = DefaultRTSPClientConfig()
	}

	if des == nil {
		des = &description.Session{}
	}

	ctx2, cancel := context.WithCancel(ctx)

	client := &RTSPClient{
		serverAddr:    config.ServerAddr,
		streamPath:    config.StreamPath,
		rtspURL:       fmt.Sprintf("rtsp://%s:%d/%s", config.ServerAddr, config.ServerPort, strings.Trim(config.StreamPath, "/")),
		description:   des,
		config:        config,
		reconnectChan: make(chan struct{}, 1),
		ctx:           ctx2,
		cancel:        cancel,
		metrics:       metrics.NewUnifiedMetrics(ctx2, "RTSP CLIENT", 10, 5*time.Second),
	}

	for _, option := range options {
		if err := option(client); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	if len(client.description.Medias) == 0 {
		return nil, errors.New("no media descriptions configured - use options to add media")
	}

	fmt.Printf("RTSP client configured for URL: %s\n", client.rtspURL)

	return client, nil
}

func (c *RTSPClient) Start() {
	c.wg.Add(1)
	go c.connect()
}

func (c *RTSPClient) WaitForConnection(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if c.metrics.GetState() == metrics.ConnectedState {
				return nil
			}

			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (c *RTSPClient) StartAndWait() <-chan struct{} {
	c.Start()
	return c.ctx.Done()
}

func (c *RTSPClient) connect() {
	defer c.wg.Done()

	attempt := 0
	currentDelay := c.config.ReconnectDelay
	maxAttempts := c.config.ReconnectAttempts

	for {
		select {
		case <-c.ctx.Done():
			fmt.Printf("RTSP connection manager stopping due to context cancellation\n")
			c.metrics.SetState(metrics.DisconnectedState)
			return
		default:
		}

		c.metrics.SetState(metrics.ConnectingState)
		fmt.Printf("Attempting to connect to RTSP server: %s\n", c.rtspURL)

		if err := c.attemptConnection(); err != nil {
			c.metrics.SetState(metrics.ErrorState)
			c.metrics.AddErrors(err)
			fmt.Printf("RTSP connection failed: %v\n", err)

			if maxAttempts == 0 {
				fmt.Printf("No retries configured, stopping connection attempts\n")
				c.metrics.SetState(metrics.DisconnectedState)
				return
			}

			if maxAttempts > 0 && attempt >= maxAttempts {
				fmt.Printf("Maximum retry attempts (%d) reached, stopping\n", maxAttempts)
				c.metrics.SetState(metrics.DisconnectedState)
				return
			}

			fmt.Printf("Retrying RTSP connection in %v (attempt %d)\n", currentDelay, attempt+1)
			select {
			case <-c.ctx.Done():
				fmt.Printf("RTSP connection manager stopping during retry delay\n")
				c.metrics.SetState(metrics.DisconnectedState)
				return
			case <-time.After(currentDelay):
				// Continue to next attempt
			}

			currentDelay = time.Duration(float64(currentDelay) * 1.5)
			if currentDelay > 30*time.Second {
				currentDelay = 30 * time.Second
			}

			attempt++
			continue
		}

		fmt.Printf("RTSP connection established, now recording to: %s\n", c.rtspURL)
		currentDelay = c.config.ReconnectDelay // Reset backoff delay
		attempt = 0                            // Reset attempt counter

		c.monitorConnection()

		select {
		case <-c.ctx.Done():
			fmt.Printf("RTSP connection manager stopping - context cancelled\n")
			c.metrics.SetState(metrics.DisconnectedState)
			return
		default:
			fmt.Printf("RTSP connection lost, attempting to reconnect...\n")
			c.metrics.SetState(metrics.ConnectingState)
		}
	}
}

func (c *RTSPClient) attemptConnection() error {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.client = &gortsplib.Client{
		ReadTimeout:    c.config.ReadTimeout,
		WriteTimeout:   c.config.WriteTimeout,
		UserAgent:      c.config.UserAgent,
		WriteQueueSize: c.config.WriteQueueSize, // added for ffmpeg-c-api-bitrate-update branch; todo: remove later
	}

	if err := c.client.StartRecording(c.rtspURL, c.description); err != nil {
		return fmt.Errorf("RTSP protocol negotiation failed: %w", err)
	}

	c.metrics.SetState(metrics.ConnectedState)
	c.metrics.SetLastWriteTime(time.Now())

	return nil
}

func (c *RTSPClient) monitorConnection() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.reconnectChan:
			return
		case <-ticker.C:
			if time.Since(c.metrics.GetLastWriteTime()) > 60*time.Second {
				fmt.Printf("RTSP connection appears stale (no writes for 60s)\n")
			}
		}
	}
}

func (c *RTSPClient) Consume(_ context.Context, packet *rtp.Packet) error {
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	default:
		if packet == nil {
			// Gracefully handle nil packets (common in some pipeline scenarios)
			return nil
		}

		state := c.metrics.GetState()
		if state != metrics.ConnectedState {
			fmt.Printf("cannot transmit packet: client state is %s, expected %s (recording)\n", state.String(), metrics.ConnectedState.String())
			return nil
		}

		c.mux.RLock()
		defer c.mux.RUnlock()

		if c.client == nil {
			return errors.New("RTSP client not initialized")
		}

		if len(c.description.Medias) == 0 || len(c.description.Medias[0].Formats) == 0 {
			return errors.New("no media formats configured")
		}

		expectedPayloadType := c.description.Medias[0].Formats[0].PayloadType()
		if packet.PayloadType != expectedPayloadType {
			fmt.Printf("Correcting RTP payload type: expected %d, got %d\n",
				expectedPayloadType, packet.PayloadType)
			packet.PayloadType = expectedPayloadType
		}

		if err := c.client.WritePacketRTP(c.description.Medias[0], packet); err != nil {
			c.metrics.SetState(metrics.ErrorState)
			c.metrics.AddErrors(err)

			select {
			case c.reconnectChan <- struct{}{}:
				fmt.Printf("Triggered RTSP reconnection due to write error\n")
			default:
				// Channel full, reconnection already pending
			}

			fmt.Printf("failed to write RTP packet to RTSP server: %v\n", err)
			return nil
		}

		c.metrics.IncrementPacketsWritten()
		c.metrics.IncrementBytesWritten(uint64(len(packet.Payload)))
		c.metrics.SetLastWriteTime(time.Now())

		return nil
	}
}

func (c *RTSPClient) AppendRTSPMediaDescription(media *description.Media) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.description.Medias = append(c.description.Medias, media)
	fmt.Printf("Added media description to RTSP process (total: %d)\n", len(c.description.Medias))
}

func (c *RTSPClient) GetH264Parameters() (sps, pps []byte, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	for _, media := range c.description.Medias {
		if media.Type == description.MediaTypeVideo {
			for _, _format := range media.Formats {
				if f, ok := _format.(*format.H264); ok {
					return f.SPS, f.PPS, nil
				}
			}
		}
	}

	return nil, nil, fmt.Errorf("no H264 description found")
}

func (c *RTSPClient) Close() error {
	fmt.Printf("Shutting down RTSP client for %s\n", c.rtspURL)

	if c.cancel != nil {
		c.cancel()
	}

	c.wg.Wait()

	c.mux.Lock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.mux.Unlock()

	c.metrics.SetState(metrics.DisconnectedState)
	fmt.Printf("RTSP client shutdown complete\n")

	return nil
}
