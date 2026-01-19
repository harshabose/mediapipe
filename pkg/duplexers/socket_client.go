package duplexers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/tools/pkg/metrics"
)

var (
	ErrSocketConnectionNotReady = errors.New("reader not ready yet")
	ErrSocketWrite              = errors.New("cannot write data in current state")
	ErrSocketRead               = errors.New("cannot read data in current state")
)

// SocketClientConfig contains all configuration parameters for the WebSocket client.
// This includes connection details, authentication credentials, io timeouts,
// and retry behavior.
//
// Note: AccessToken is transmitted via query parameter for maximum client compatibility,
// though this is less secure than header-based authentication. Ensure HTTPS is used
// in production environments to protect credentials in transit.
type SocketClientConfig struct {
	// Connection settings
	Domain string // WebSocket server address (e.g., "wss://example.com")
	Port   uint16 // WebSocket server port
	Path   string

	// Authentication settings // NOT IMPLEMENTED FOR NOW
	// AuthURL  string // Authentication service URL for login
	// Username string // Username for authentication
	// Password string // Password for authentication

	// WebSocket protocol settings
	MessageType websocket.MessageType // Type of WebSocket messages (Binary/Text)

	// Connection management
	ShouldReconnectIfError      bool          // Whether to maintain a persistent connection
	MaxReconnectionAttempts     uint8         // Maximum reconnection attempts (0 = unlimited)
	ReconnectionAttemptDelay    time.Duration // Initial delay between reconnection attempts
	MaxReconnectionAttemptDelay time.Duration // Maximum delay
}

func (c *SocketClientConfig) updateDelay() {
	newDelay := time.Duration(float64(c.ReconnectionAttemptDelay) * 1.5)
	if newDelay > c.MaxReconnectionAttemptDelay {
		newDelay = c.MaxReconnectionAttemptDelay
	}
	c.ReconnectionAttemptDelay = newDelay
}

func (c *SocketClientConfig) shouldRetry(attempt uint8) bool {
	if !c.ShouldReconnectIfError {
		fmt.Println("not retrying as not configured to reconnect")
		return false
	}

	if c.MaxReconnectionAttempts == 0 {
		fmt.Printf("no retries configured, stopping connection attempts\n")
		return false
	}

	if c.MaxReconnectionAttempts > 0 && attempt >= c.MaxReconnectionAttempts {
		fmt.Printf("maximum retry attempts (%d) reached, stopping\n", c.MaxReconnectionAttempts)
		return false
	}

	return true
}

func (c *SocketClientConfig) wait(ctx context.Context, attempt uint8) bool {
	fmt.Printf("retrying socket connection in %v (attempt %d)\n", c.ReconnectionAttemptDelay, attempt+1)

	select {
	case <-ctx.Done():
		return false
	case <-time.After(c.ReconnectionAttemptDelay):
		return true
	}
}

type SocketClient struct {
	conn   *Websocket
	config SocketClientConfig // SocketClient configuration parameters

	metrics *metrics.UnifiedMetrics // Operational metrics and statistics

	// Concurrency and lifecycle management
	once      sync.Once          // Ensures Close() is idempotent
	ctx       context.Context    // SocketClient context for cancellation
	cancel    context.CancelFunc // Function to cancel client operations
	mux       sync.RWMutex       // Protects reader/writer access
	wg        sync.WaitGroup     // Synchronises background goroutines
	reconnect chan struct{}      // Channel for triggering reconnection
}

func NewSocketClient(ctx context.Context, config SocketClientConfig) *SocketClient {
	ctx2, cancel2 := context.WithCancel(ctx)

	c := &SocketClient{
		conn:      nil,
		config:    config,
		metrics:   metrics.NewUnifiedMetrics(ctx2, "WEBSOCKET", 10, 5*time.Second),
		reconnect: make(chan struct{}, 1),
		ctx:       ctx2,
		cancel:    cancel2,
	}

	return c
}

func (c *SocketClient) Connect() {
	go c.connect()
}

func (c *SocketClient) WaitForConnection(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(timeout / 10)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:

			c.mux.RLock()
			connected := c.conn != nil
			c.mux.RUnlock()

			if connected && c.metrics.GetState() == metrics.ConnectedState {
				return nil
			}
		}
	}
}

func (c *SocketClient) Done() <-chan struct{} {
	return c.ctx.Done()
}

func (c *SocketClient) connect() {
	c.wg.Add(1)
	defer c.wg.Done()

	defer c.metrics.SetState(metrics.DisconnectedState)

	var attempt uint8 = 0

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			c.metrics.SetState(metrics.ConnectingState)

			conn, err := c.attemptConnection()
			if err != nil {
				c.metrics.SetState(metrics.ErrorState)
				c.metrics.AddErrors(err)

				if !c.config.shouldRetry(attempt) {
					return
				}

				if !c.config.wait(c.ctx, attempt) {
					return
				}

				c.config.updateDelay()

				attempt++
				continue
			}

			// Connection successful
			c.setConn(NewWebSocket(conn, c.config.MessageType))
			c.metrics.SetState(metrics.ConnectedState)

			c.monitorConnection()
			c.metrics.SetState(metrics.DisconnectedState)

			if !c.config.shouldRetry(attempt) {
				return
			}

			if !c.config.wait(c.ctx, attempt) {
				return
			}

			c.config.updateDelay()
			attempt++
		}
	}
}

func (c *SocketClient) setConn(conn *Websocket) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.conn = conn
}

func (c *SocketClient) monitorConnection() {
	c.wg.Add(1)
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.reconnect:
			fmt.Println("received reconnection request: triggering websocket reconnection...")
			return
		case <-ticker.C:
			if time.Since(c.metrics.GetLastWriteTime()) > 60*time.Second || time.Since(c.metrics.GetLastReadTime()) > 60*time.Second {
				fmt.Printf("socket connection appears stale (no writes and/or read for 60s)\n")
			}
		}
	}
}

func (c *SocketClient) attemptConnection() (*websocket.Conn, error) {
	// removed; needs more development
	// if err := c.performLogin(); err != nil {
	// 	return nil, err
	// }

	// token, ok := c.auth.GetCurrentToken()
	// if !ok {
	// 	return errors.New("token is not valid")
	// }

	// conn, _, err := websocket.Dial(c.ctx, fmt.Sprintf("%s:%d?token=%s", c.config.Domain, c.config.Port, token), nil)
	// if err != nil {
	// 	return err
	// }

	url := fmt.Sprintf("%s:%d%s", c.config.Domain, c.config.Port, c.config.Path)
	conn, _, err := websocket.Dial(c.ctx, url, nil)
	if err != nil {
		return nil, err
	}

	return conn, err
}

func (c *SocketClient) Generate(ctx context.Context) ([]byte, error) {
	select {
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-ctx.Done():
		return nil, fmt.Errorf("error while calling Generate on SocketClient: %w", ctx.Err())
	default:
		if c.metrics.GetState() != metrics.ConnectedState {
			c.metrics.AddErrors(fmt.Errorf("cannot transmit data: client state is %s, expected %s (connected)", c.metrics.GetState().String(), metrics.ConnectedState.String()))
			return nil, ErrSocketWrite
		}

		c.mux.RLock()
		defer c.mux.RUnlock()

		if c.conn == nil {
			c.metrics.AddErrors(ErrSocketConnectionNotReady)
			return nil, ErrSocketConnectionNotReady
		}

		data, err := c.conn.Generate(ctx)
		if err != nil {
			c.metrics.SetState(metrics.ErrorState)
			c.metrics.AddErrors(fmt.Errorf("failed to read data from websocket server: %w", err))

			c.triggerReconnection()

			return nil, ErrSocketWrite
		}

		c.metrics.IncrementPacketsRead()
		c.metrics.IncrementBytesRead(uint64(len(data)))
		c.metrics.SetLastReadTime(time.Now())

		return data, nil
	}
}

func (c *SocketClient) triggerReconnection() {
	select {
	case c.reconnect <- struct{}{}:
	default:
		// Channel full, reconnection already pending
	}
}

func (c *SocketClient) Consume(ctx context.Context, data []byte) error {
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-ctx.Done():
		c.metrics.AddErrors(ctx.Err())
		return ctx.Err()
	default:
		if c.metrics.GetState() != metrics.ConnectedState {
			c.metrics.AddErrors(fmt.Errorf("cannot transmit data: client state is %s, expected %s (connected)", c.metrics.GetState().String(), metrics.ConnectedState.String()))
			return ErrSocketRead
		}

		c.mux.RLock()
		defer c.mux.RUnlock()

		if c.conn == nil {
			c.metrics.AddErrors(ErrSocketConnectionNotReady)
			return ErrSocketConnectionNotReady
		}

		if err := c.conn.Consume(ctx, data); err != nil {
			c.metrics.SetState(metrics.ErrorState)
			c.metrics.AddErrors(fmt.Errorf("failed to write data to websocket server: %w", err))

			c.triggerReconnection()

			return ErrSocketRead
		}

		c.metrics.IncrementPacketsWritten()
		c.metrics.IncrementBytesWritten(uint64(len(data)))
		c.metrics.SetLastWriteTime(time.Now())

		return nil
	}
}

func (c *SocketClient) GetMetrics() metrics.Snapshot {
	return c.metrics.GetSnapshot()
}

// performLogin authenticates with the configured auth service and obtains
// JWT tokens for WebSocket authentication.
// func (c *SocketClient) performLogin() error {
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

func (c *SocketClient) Close() error {
	var err error = nil

	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}

		c.wg.Wait()

		c.mux.Lock()
		defer c.mux.Unlock()

		err = c.conn.Close()
		close(c.reconnect)
	})

	return err
}
