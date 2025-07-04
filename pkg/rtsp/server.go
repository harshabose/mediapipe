package rtsp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

type ServerState int

const (
	ServerDownState  ServerState = iota
	ServerSettingUp  ServerState = iota
	ServerUpState    ServerState = iota
	ServerErrorState ServerState = iota
)

func stateToString(state ServerState) string {
	switch state {
	case ServerDownState:
		return "DOWN"
	case ServerSettingUp:
		return "SETTING_UP"
	case ServerUpState:
		return "UP"
	case ServerErrorState:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

type ServerConfig struct {
	Port                    int
	MaxClients              int
	MaxStreams              int
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	TLSConfig               *tls.Config
	PublisherSessionTimeout time.Duration
	ClientSessionTimeout    time.Duration
	AllowLocalOnly          bool
	UDPRTPAddress           string
	UDPRTCPAddress          string
	MulticastIPRange        string
	MulticastRTPPort        int
	MulticastRTCPPort       int
	WriteQueueSize          int
	ReServeAttempts         int
	ReServerDelay           time.Duration
	MetricsPrintInterval    time.Duration

	ReadRequiredScopes  []string
	ReadRequiredRoles   []string
	WriteRequiredScopes []string
	WriteRequiredRoles  []string

	AuthURL    string
	AuthAPIKey string
}

func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Port:                    8554,
		MaxClients:              100,
		MaxStreams:              10,
		ReadTimeout:             1 * time.Second,
		WriteTimeout:            1 * time.Second,
		TLSConfig:               nil,
		PublisherSessionTimeout: 60 * time.Second,
		ClientSessionTimeout:    60 * time.Second,
		AllowLocalOnly:          false,
		UDPRTPAddress:           ":8000",
		UDPRTCPAddress:          ":8001",
		MulticastIPRange:        "224.1.0.0/16",
		MulticastRTPPort:        8002,
		MulticastRTCPPort:       8003,
		WriteQueueSize:          256,
		ReServeAttempts:         30,
		ReServerDelay:           3 * time.Second,
		MetricsPrintInterval:    30 * time.Second,

		ReadRequiredScopes:  []string{"rtsp-skyline-sonata"},
		ReadRequiredRoles:   []string{"user", "moderator", "admin", "viewer", "guest"},
		WriteRequiredScopes: []string{"rtsp-skyline-sonata"},
		WriteRequiredRoles:  []string{"user", "moderator", "admin"},

		AuthURL:    "",
		AuthAPIKey: "",
	}
}

type ClientSession struct {
	ID         string
	Session    *gortsplib.ServerSession
	RemoteAddr string
	IsLocal    bool
	ConnTime   time.Time
	LastActive time.Time
}

type StreamInfo struct {
	Stream              *gortsplib.ServerStream
	Publisher           *gortsplib.ServerSession
	PublisherLastActive time.Time
	Description         *description.Session
	Clients             map[string]*ClientSession
	CreatedAt           time.Time
	mux                 sync.RWMutex
}

func (si *StreamInfo) AddClient(client *ClientSession) {
	si.mux.Lock()
	defer si.mux.Unlock()

	si.Clients[client.ID] = client
}

func (si *StreamInfo) RemoveClient(clientID string) {
	si.mux.Lock()
	defer si.mux.Unlock()

	delete(si.Clients, clientID)
}

func (si *StreamInfo) GetClientCount() int {
	si.mux.RLock()
	defer si.mux.RUnlock()

	return len(si.Clients)
}

func (si *StreamInfo) GetClients() []*ClientSession {
	si.mux.RLock()
	defer si.mux.RUnlock()

	clients := make([]*ClientSession, 0, len(si.Clients))
	for _, client := range si.Clients {
		clients = append(clients, client)
	}
	return clients
}

type ServerMetrics struct {
	State            ServerState   `json:"state"`
	TotalConnections uint64        `json:"total_connections"`
	TotalStreams     uint64        `json:"total_streams"`
	RecentErrors     []string      `json:"recent_errors"`
	LastUpdate       time.Time     `json:"last_update"`
	TotalUptime      time.Duration `json:"total_uptime"`

	maxErrorCount uint8
	mux           sync.RWMutex
}

func (m *ServerMetrics) SetState(state ServerState) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.State = state
}

func (m *ServerMetrics) IncrementTotalConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalConnections++
}

func (m *ServerMetrics) DecrementTotalConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalConnections--
}

func (m *ServerMetrics) ResetTotalConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalConnections = 0
}

func (m *ServerMetrics) GetTotalConnections() uint64 {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.TotalConnections
}

func (m *ServerMetrics) IncrementTotalStreams() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalStreams++
}

func (m *ServerMetrics) DecrementTotalStreams() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalStreams--
}

func (m *ServerMetrics) ResetTotalStreams() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.LastUpdate = time.Now()
	m.TotalStreams = 0
}

func (m *ServerMetrics) AddError(err error) {
	m.mux.Lock()
	defer m.mux.Unlock()

	if uint8(len(m.RecentErrors)) > m.maxErrorCount {
		m.RecentErrors = m.RecentErrors[1:]
	}

	m.LastUpdate = time.Now()
	m.RecentErrors = append(m.RecentErrors, err.Error())
}

func isLocalhost(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return strings.ToLower(host) == "localhost"
	}

	return ip.IsLoopback()
}

type Server struct {
	server *gortsplib.Server
	config *ServerConfig

	// Stream management
	streams map[string]*StreamInfo
	mux     sync.RWMutex

	// Context management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	metrics *ServerMetrics
}

func NewServer(ctx context.Context, config *ServerConfig) *Server {
	if config == nil {
		config = DefaultServerConfig()
	}

	ctx2, cancel := context.WithCancel(ctx)

	server := &Server{
		config:  config,
		streams: make(map[string]*StreamInfo),
		ctx:     ctx2,
		metrics: &ServerMetrics{maxErrorCount: 10},
		cancel:  cancel,
	}

	server.server = &gortsplib.Server{
		Handler:           server,
		RTSPAddress:       fmt.Sprintf("0.0.0.0:%d", config.Port),
		TLSConfig:         config.TLSConfig,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		WriteQueueSize:    config.WriteQueueSize,
		UDPRTPAddress:     config.UDPRTPAddress,
		UDPRTCPAddress:    config.UDPRTCPAddress,
		MulticastIPRange:  config.MulticastIPRange,  // NOTE: NOT NEEDED
		MulticastRTPPort:  config.MulticastRTPPort,  // NOTE: NOT NEEDED
		MulticastRTCPPort: config.MulticastRTCPPort, // NOTE: NOT NEEDED
	}

	return server
}

func (s *Server) Serve() {
	s.wg.Add(3)
	go s.connectionRoutine()
	go s.cleanupRoutine()
	go s.printMetrics()

	s.metrics.SetState(ServerUpState)
}

func (s *Server) ServeAndWait() <-chan struct{} {
	s.Serve()

	return s.ctx.Done()
}

func (s *Server) cleanupRoutine() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.cleanupInactiveSessions()
		}
	}
}

func (s *Server) cleanupInactiveSessions() {
	s.mux.Lock()
	defer s.mux.Unlock()

	now := time.Now()
	for path, streamInfo := range s.streams {
		streamInfo.mux.Lock()

		publisherInactive := now.Sub(streamInfo.PublisherLastActive) > s.config.PublisherSessionTimeout

		if publisherInactive {
			// Publisher inactive - close stream
			streamInfo.mux.Unlock()

			fmt.Printf("Publisher inactive for stream %s, closing stream\n", path)
			if streamInfo.Stream != nil {
				streamInfo.Stream.Close()
			}
			if streamInfo.Publisher != nil {
				streamInfo.Publisher.Close()
			}
			delete(s.streams, path)
			s.metrics.DecrementTotalStreams()

			continue
		}

		toRemove := make([]string, 0)
		for clientID, client := range streamInfo.Clients {
			if now.Sub(client.LastActive) > s.config.ClientSessionTimeout {
				toRemove = append(toRemove, clientID)
			}
		}

		for _, clientID := range toRemove {
			delete(streamInfo.Clients, clientID)
			s.metrics.DecrementTotalConnections()

			fmt.Printf("Removed inactive client %s from stream %s\n", clientID, path)
		}

		streamInfo.mux.Unlock()
	}
}

func (s *Server) connectionRoutine() {
	defer s.wg.Done()

	attempt := 0
	currentDelay := s.config.ReServerDelay
	maxAttempts := s.config.ReServeAttempts

	for {
		s.metrics.SetState(ServerSettingUp)

		select {
		case <-s.ctx.Done():
			fmt.Printf("RTSP server connection manager stopping due to context cancellation\n")
			s.metrics.SetState(ServerDownState)
			return
		default:
			fmt.Printf("Attempting to start the RTSP server: 0.0.0.0:%d\n", s.config.Port)

			s.metrics.SetState(ServerUpState)
			if err := s.server.StartAndWait(); err != nil {
				s.metrics.SetState(ServerErrorState)
				s.metrics.AddError(err)
				fmt.Printf("RTSP server start failed: %v\n", err)

				if maxAttempts == 0 {
					fmt.Printf("No retries configured, stopping server start attempts\n")
					s.metrics.SetState(ServerDownState)
					return
				}

				if maxAttempts > 0 && attempt >= maxAttempts {
					fmt.Printf("Maximum retry attempts (%d) reached, stopping\n", maxAttempts)
					s.metrics.SetState(ServerDownState)
					return
				}

				fmt.Printf("Retyrying RTSP server start in %v (attempt %d)\n", currentDelay, attempt+1)
				select {
				case <-s.ctx.Done():
					fmt.Printf("RTSP connection manager stopping during retry delay\n")
					s.metrics.SetState(ServerDownState)
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
		}
	}
}

func (s *Server) printMetrics() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.MetricsPrintInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			fmt.Printf("Metrics printing is stopped due to context cancellation")
			return
		case <-ticker.C:
			s.printDetailedMetrics()
		}
	}
}

func (s *Server) printDetailedMetrics() {
	s.metrics.mux.RLock()
	state := s.metrics.State
	totalConns := s.metrics.TotalConnections
	totalStreams := s.metrics.TotalStreams
	uptime := s.metrics.TotalUptime
	lastUpdate := s.metrics.LastUpdate
	errors := append([]string{}, s.metrics.RecentErrors...)
	s.metrics.mux.RUnlock()

	fmt.Printf("\n=== RTSP Server Metrics [%s] ===\n", time.Now().Format("15:04:05"))
	fmt.Printf("Server State: %s\n", stateToString(state))
	fmt.Printf("Total Connections: %d\n", totalConns)
	fmt.Printf("Total Streams: %d\n", totalStreams)
	fmt.Printf("Uptime: %v\n", uptime.Round(time.Second))
	fmt.Printf("Last Update: %v ago\n", time.Since(lastUpdate).Round(time.Second))

	// Print active streams
	s.mux.RLock()
	if len(s.streams) > 0 {
		fmt.Printf("\nActive Streams:\n")
		for path, streamInfo := range s.streams {
			clientCount := streamInfo.GetClientCount()
			duration := time.Since(streamInfo.CreatedAt).Round(time.Second)
			fmt.Printf("  %s: %d clients, active for %v\n", path, clientCount, duration)
		}
	} else {
		fmt.Printf("\nActive Streams: None\n")
	}
	s.mux.RUnlock()

	// Print recent errors
	if len(errors) > 0 {
		fmt.Printf("\nRecent Errors:\n")
		for i, err := range errors {
			fmt.Printf("  %d. %s\n", i+1, err)
		}
	}

	fmt.Printf("=====================================\n")
}

func (s *Server) Close() error {
	fmt.Println("Stopping RTSP prod...")

	// Close all streams
	if s.cancel == nil {
		return nil
	}
	s.cancel()

	s.mux.Lock()

	for path, streamInfo := range s.streams {
		if streamInfo.Stream != nil {
			streamInfo.Stream.Close()
		}
		if streamInfo.Publisher != nil {
			streamInfo.Publisher.Close()
		}
		fmt.Printf("Closed stream: %s\n", path)
	}
	s.streams = make(map[string]*StreamInfo)
	s.mux.Unlock()

	// Stop prod
	if s.server != nil {
		s.server.Close()
	}

	// Wait for background routines
	s.wg.Wait()

	s.metrics.ResetTotalStreams()
	s.metrics.ResetTotalConnections()

	fmt.Println("RTSP prod stopped")
	return nil
}

func (s *Server) validateConnection(remoteAddr string) error {
	if s.config.AllowLocalOnly && !isLocalhost(remoteAddr) {
		return fmt.Errorf("only localhost connections allowed")
	}

	// Check max clients across all streams
	s.mux.RLock()
	totalClients := 0
	for _, streamInfo := range s.streams {
		totalClients += streamInfo.GetClientCount()
	}
	s.mux.RUnlock()

	if totalClients >= s.config.MaxClients {
		return fmt.Errorf("maximum client limit reached")
	}

	return nil
}

// OnConnOpen is called when a publisher/client completes the TCP handshake
func (s *Server) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	if err := s.validateConnection(ctx.Conn.NetConn().RemoteAddr().String()); err != nil {
		fmt.Printf("Connection rejected: %v\n", err)
		ctx.Conn.Close()
		s.metrics.AddError(err)
		return
	}

	s.metrics.IncrementTotalConnections()

	fmt.Printf("Connection opened from %s (total: %d)\n", ctx.Conn.NetConn().RemoteAddr(), s.metrics.GetTotalConnections())
}

// OnConnClose is called when a publisher/client disconnects TCP
func (s *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	s.metrics.DecrementTotalConnections()

	fmt.Printf("Connection closed from %s: %v\n", ctx.Conn.NetConn().RemoteAddr(), ctx.Error)
}

// OnSessionOpen is called after OnConnOpen and indicates RTSP session start.
func (s *Server) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	clientID := fmt.Sprintf("%s-%d", ctx.Conn.NetConn().RemoteAddr(), time.Now().UnixNano())
	fmt.Printf("Session opened: %s from %s\n", clientID, ctx.Conn.NetConn().RemoteAddr())

	// Store client info in session
	ctx.Session.SetUserData(map[string]interface{}{
		"clientID":   clientID,
		"remoteAddr": ctx.Conn.NetConn().RemoteAddr().String(),
		"isLocal":    isLocalhost(ctx.Conn.NetConn().RemoteAddr().String()),
		"connTime":   time.Now(),
	})
}

// OnSessionClose is called after OnConnClose and indicates RTSP session close.
func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	userData := ctx.Session.UserData()
	if userData == nil {
		return
	}

	userMap, ok := userData.(map[string]interface{})
	if !ok {
		return
	}

	clientID, _ := userMap["clientID"].(string)
	fmt.Printf("Session closed: %s\n", clientID)

	s.mux.Lock()
	defer s.mux.Unlock()

	// Remove client from all streams
	for path, streamInfo := range s.streams {
		streamInfo.RemoveClient(clientID)

		// If this was the publisher, close the stream
		if streamInfo.Publisher == ctx.Session {
			if streamInfo.Stream != nil {
				streamInfo.Stream.Close()
			}
			delete(s.streams, path)
			fmt.Printf("Publisher disconnected, stream %s closed\n", path)
		}

		s.metrics.DecrementTotalStreams()
	}
}

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path := ctx.Path
	fmt.Printf("Describe request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mux.RLock()
	streamInfo, exists := s.streams[path]
	s.mux.RUnlock()

	// values, err := url.ParseQuery(ctx.Query)
	// if err != nil {
	// 	fmt.Printf("Failed to parse query: %v\n", err)
	// 	return &base.Response{
	// 		StatusCode: base.StatusBadRequest,
	// 	}, nil
	// }
	//
	// tokens, ok := values["token"]
	// if !ok || len(tokens) != 1 {
	// 	fmt.Printf("auth failed: token absent in the query or format mismatch\n")
	// 	return &base.Response{
	// 		StatusCode: base.StatusUnauthorized,
	// 	}, nil
	// }
	//
	// _ = tokens[0]

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, streamInfo.Stream, nil
}

func (s *Server) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Announce request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mux.Lock()
	defer s.mux.Unlock()

	// values, err := url.ParseQuery(ctx.Query)
	// if err != nil {
	// 	fmt.Printf("Failed to parse query: %v\n", err)
	// 	return &base.Response{
	// 		StatusCode: base.StatusBadRequest,
	// 	}, nil
	// }
	//
	// tokens, ok := values["token"]
	// if !ok || len(tokens) != 1 {
	// 	fmt.Printf("auth failed: token absent in the query or format mismatch\n")
	// 	return &base.Response{
	// 		StatusCode: base.StatusUnauthorized,
	// 	}, nil
	// }
	//
	// _ = tokens[0]

	// Check max streams limit
	if len(s.streams) >= s.config.MaxStreams {
		fmt.Println("Maximum stream limit reached")
		return &base.Response{
			StatusCode: base.StatusServiceUnavailable,
		}, nil
	}

	// Close existing stream if it exists
	if existingStream, exists := s.streams[path]; exists {
		if existingStream.Stream != nil {
			existingStream.Stream.Close()
		}
		if existingStream.Publisher != nil {
			existingStream.Publisher.Close()
		}
		fmt.Printf("Replaced existing stream: %s\n", path)
	}

	// Create a new stream
	stream := &gortsplib.ServerStream{Server: s.server, Desc: ctx.Description}
	if err := stream.Initialize(); err != nil {
		s.metrics.AddError(err)
		return &base.Response{
			StatusCode: base.StatusInternalServerError,
		}, nil
	}

	streamInfo := &StreamInfo{
		Stream:              stream,
		Publisher:           ctx.Session,
		PublisherLastActive: time.Now(),
		Description:         ctx.Description,
		Clients:             make(map[string]*ClientSession),
		CreatedAt:           time.Now(),
	}

	s.streams[path] = streamInfo
	fmt.Printf("Stream created: %s\n", path)

	s.metrics.IncrementTotalStreams()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path := ctx.Path
	fmt.Printf("Setup request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mux.RLock()
	streamInfo, exists := s.streams[path]
	s.mux.RUnlock()

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found for setup: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	isPublisher := streamInfo.Publisher == ctx.Session

	if isPublisher {
		fmt.Printf("Setup for publisher on path: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusOK,
		}, nil, nil
	}

	// Add client to stream
	userData := ctx.Session.UserData()
	if userData != nil {
		if userMap, ok := userData.(map[string]interface{}); ok {
			clientID, _ := userMap["clientID"].(string)
			remoteAddr, _ := userMap["remoteAddr"].(string)
			isLocal, _ := userMap["isLocal"].(bool)
			connTime, _ := userMap["connTime"].(time.Time)

			client := &ClientSession{
				ID:         clientID,
				Session:    ctx.Session,
				RemoteAddr: remoteAddr,
				IsLocal:    isLocal,
				ConnTime:   connTime,
				LastActive: time.Now(),
			}

			streamInfo.AddClient(client)
			fmt.Printf("Client %s added to stream %s\n", clientID, path)
		}
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, streamInfo.Stream, nil
}

func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Play request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	// Update client's last active time
	s.mux.RLock()
	if streamInfo, exists := s.streams[path]; exists {
		userData := ctx.Session.UserData()
		if userData != nil {
			if userMap, ok := userData.(map[string]interface{}); ok {
				clientID, _ := userMap["clientID"].(string)
				streamInfo.mux.Lock()
				if client, exists := streamInfo.Clients[clientID]; exists {
					client.LastActive = time.Now()
				}
				streamInfo.mux.Unlock()
			}
		}
	}
	s.mux.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Record request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mux.RLock()
	streamInfo, exists := s.streams[path]
	s.mux.RUnlock()

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found for record: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil
	}

	// Set up packet handling
	ctx.Session.OnPacketRTPAny(func(media *description.Media, format format.Format, pkt *rtp.Packet) {
		if streamInfo == nil {
			return
		}
		if err := streamInfo.Stream.WritePacketRTP(media, pkt); err != nil {
			s.metrics.AddError(err)
			fmt.Printf("Error writing RTP packet to stream %s: %v\n", path, err)
			// TODO: CONSIDER REMOVING THE STREAM
			return
		}

		// TODO: CONSIDER NOT ADDING PUBLISHER TO CLIENTS LIST AS THEIR LIFELINE NEEDS TO BE SEPARATE FROM OTHER CLIENTS
		streamInfo.mux.Lock()
		now := time.Now()

		streamInfo.PublisherLastActive = now

		for _, client := range streamInfo.Clients {
			client.LastActive = now
		}
		streamInfo.mux.Unlock()
	})

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

// GetStreamInfo returns information about a specific stream
func (s *Server) GetStreamInfo(path string) (*StreamInfo, bool) {
	s.mux.RLock()
	defer s.mux.RUnlock()

	streamInfo, exists := s.streams[path]
	return streamInfo, exists
}

// GetAllStreams returns information about all active streams
func (s *Server) GetAllStreams() map[string]*StreamInfo {
	s.mux.RLock()
	defer s.mux.RUnlock()

	result := make(map[string]*StreamInfo)
	for path, streamInfo := range s.streams {
		result[path] = streamInfo
	}
	return result
}
