package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/mediasink/pkg/https"
)

type metrics struct {
	Uptime            time.Duration `json:"uptime"`
	ActiveConnections uint64        `json:"active_connections"`
	FailedConnections uint64        `json:"failed_connections"`
	TotalDataSent     int64         `json:"total_data_sent"`
	TotalDataRecvd    int64         `json:"total_data_recvd"`
	timeSinceUptime   time.Time
	mux               sync.RWMutex
}

func (m *metrics) active() uint64 {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.ActiveConnections
}

func (m *metrics) failed() uint64 {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.FailedConnections
}

func (m *metrics) increaseActiveConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.ActiveConnections++
}

func (m *metrics) decreaseActiveConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.ActiveConnections--
}

func (m *metrics) increaseFailedConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.FailedConnections++
}

func (m *metrics) addDataSent(len int64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.TotalDataSent = m.TotalDataSent + len
}

func (m *metrics) resetUptime() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.timeSinceUptime = time.Now()
}

func (m *metrics) updateUptime() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.Uptime = time.Since(m.timeSinceUptime)
}

func (m *metrics) addDataRecvd(len int64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.TotalDataRecvd = m.TotalDataRecvd + len
}

func (m *metrics) Marshal() ([]byte, error) {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return json.Marshal(m)
}

type ServerConfig struct {
	TotalConnections   uint64   `json:"total_connections"`
	WriteRequiredScope []string `json:"write_required_scope"`
	WriteRequiredRoles []string `json:"write_required_roles"`
	ReadRequiredScope  []string `json:"read_required_scope"`
	ReadRequiredRoles  []string `json:"read_required_roles"`

	AllowedRooms  []string `json:"allowed_rooms"`  // nil (not empty) means allow all
	AllowedTopics []string `json:"allowed_topics"` // nil (not empty) means allow all

	WriteMessageType websocket.MessageType `json:"write_message_type"`
	WriteTimeout     time.Duration         `json:"write_timeout"`
	ReadTimeout      time.Duration         `json:"read_timeout"`
}

func DefaultServerConfig() ServerConfig {
	c := ServerConfig{}
	c.SetDefaults()

	return c
}

func (c *ServerConfig) SetDefaults() {
	if c.TotalConnections == 0 {
		c.TotalConnections = 100 // default max connections
	}

	if len(c.WriteRequiredScope) == 0 {
		c.WriteRequiredScope = []string{"socket-skyline-sonata"}
	}

	if len(c.WriteRequiredRoles) == 0 {
		c.WriteRequiredRoles = []string{"user", "moderator", "admin"}
	}

	if len(c.ReadRequiredScope) == 0 {
		c.ReadRequiredScope = []string{"rtsp-skyline-sonata"}
	}

	if len(c.ReadRequiredRoles) == 0 {
		c.ReadRequiredRoles = []string{"user", "moderator", "admin", "viewer", "guest"}
	}

	if len(c.AllowedRooms) == 0 {
		c.AllowedRooms = nil // nil means allow all
	}

	if len(c.AllowedTopics) == 0 {
		c.AllowedTopics = nil // nil means allow all
	}

	if c.WriteMessageType == 0 {
		c.WriteMessageType = websocket.MessageBinary // default to text messages
	}

	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Minute // default 4KB write buffer
	}

	if c.ReadTimeout == 0 {
		c.ReadTimeout = 10 * time.Minute // default 4KB read buffer
	}
}

type Server struct {
	httpServer *https.Server

	config ServerConfig

	paths map[string]*Pipe

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	mux    sync.RWMutex

	metrics *metrics
}

func NewServer(ctx context.Context, config ServerConfig, httpsConfig https.Config) *Server {
	// TODO: ADD SOCKET RELATED HEADERS AND METHODS HERE TO httpspConfig
	httpsConfig.AddAllowedHeaders("Connections", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol", "Sec-WebSocket-Accept")

	ctx2, cancel := context.WithCancel(ctx)

	s := &Server{
		httpServer: https.NewHTTPSServer(ctx, httpsConfig),
		config:     config,
		metrics:    &metrics{},
		paths:      make(map[string]*Pipe),
		ctx:        ctx2,
		cancel:     cancel,
	}

	// s.httpServer.AddRequestHandler("/ws/write/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(
	// 	s.httpServer.AuthMiddlewareWithRequiredRoomExtraction(s.wsWriteHandler, s.config.WriteRequiredScope, s.config.WriteRequiredRoles, auth.RoomPathExtractor("room")), true))))
	// s.httpServer.AddRequestHandler("/ws/read/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(
	// 	s.httpServer.AuthMiddlewareWithRequiredRoomExtraction(s.wsReadHandler, s.config.ReadRequiredScope, s.config.ReadRequiredRoles, auth.RoomPathExtractor("room")), true))))

	s.httpServer.AddRequestHandler("GET /ws/write/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(s.wsWriteHandler, true))))
	s.httpServer.AddRequestHandler("GET /ws/read/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(s.wsReadHandler, true))))

	s.httpServer.AddRequestHandler("GET /metrics", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(
		s.httpServer.InternalAuthMiddleware(s.httpServer.RateLimitMiddleware(s.metricsHandler, false)))))

	return s
}

func (s *Server) Serve() {
	s.httpServer.Serve()
}

func (s *Server) StartAndWait() <-chan struct{} {
	s.Serve()
	fmt.Printf("starting websocket server on\n")
	return s.ctx.Done()
}

func (s *Server) UpgradeRequest(w http.ResponseWriter, req *http.Request) (*websocket.Conn, error) {
	if s.metrics.active()+1 > s.config.TotalConnections {
		s.metrics.increaseFailedConnections()
		fmt.Printf("current number of clients: %d; max allowed: %d\n", s.metrics.active(), s.config.TotalConnections)
		return nil, errors.New("max clients reached")
	}
	s.metrics.increaseActiveConnections()

	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		s.metrics.decreaseActiveConnections()
		s.metrics.increaseFailedConnections()
		return nil, fmt.Errorf("error while upgrading http request to websocket; err: %s", err.Error())
	}

	return conn, nil
}

func (s *Server) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	s.metrics.updateUptime()

	msg, err := s.metrics.Marshal()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to marshal metrics: %s", err.Error())
		s.httpServer.AppendErrors(errMsg)
		http.Error(w, "Failed to marshal metrics", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(msg); err != nil {
		errMsg := fmt.Sprintf("Error while sending metrics response: %s", err.Error())
		fmt.Println(errMsg)
		s.httpServer.AppendErrors(errMsg)
		return
	}
}

func (s *Server) wsWriteHandler(w http.ResponseWriter, r *http.Request) {
	s.WsWriteHandler(w, r)
}

func (s *Server) WsWriteHandler(w http.ResponseWriter, r *http.Request) {
	defer fmt.Println("exiting ws writer handler")
	room, err := GetPathVariable(r, "room")
	if err != nil {
		http.Error(w, "invalid room path parameter", http.StatusBadRequest)
		return
	}

	if s.config.AllowedRooms != nil {
		allowed := false
		for _, allowedRoom := range s.config.AllowedRooms {
			if room == allowedRoom {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "room not allowed", http.StatusForbidden)
			return
		}
	}

	topic, err := GetPathVariable(r, "topic")
	if err != nil {
		http.Error(w, "topic parameter required", http.StatusBadRequest)
		return
	}

	if s.config.AllowedTopics != nil {
		allowed := false
		for _, allowedTopic := range s.config.AllowedTopics {
			if topic == allowedTopic {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "topic not allowed", http.StatusForbidden)
			return
		}
	}

	path := room + "/" + topic

	fmt.Printf("request for %s\n", path)

	if err := s.ExistsPath(path); err != nil {
		http.Error(w, "Resource already exists", http.StatusInternalServerError)
		return
	}

	conn, err := s.UpgradeRequest(w, r)
	if err != nil {
		fmt.Printf("error while updating websocket: %v\n", err)
		http.Error(w, "Failed to upgrade to websocket", http.StatusInternalServerError)
		return
	}

	fmt.Println("upgrade successful")

	connection := NewConnection(r.Context(), conn, s.config.WriteMessageType, s.config.ReadTimeout, s.config.WriteTimeout)

	defer fmt.Println("deleted path")
	defer s.RemovePath(path)
	defer fmt.Println("ws writer connection closed")
	defer connection.Close()

	s.AddPath(path, NewPipe(connection))

	fmt.Println("readers and writers set")

	s.paths[path].WaitUntilFatal()
}

func (s *Server) wsReadHandler(w http.ResponseWriter, r *http.Request) {
	s.WsReadHandler(w, r)
}

func (s *Server) WsReadHandler(w http.ResponseWriter, r *http.Request) {
	defer fmt.Println("exit ws reader connection")
	room, err := GetPathVariable(r, "room")
	if err != nil {
		http.Error(w, "invalid room path parameter", http.StatusBadRequest)
		return
	}

	topic, err := GetPathVariable(r, "topic")
	if err != nil {
		http.Error(w, "topic parameter required", http.StatusBadRequest)
		return
	}
	path := room + "/" + topic

	pipe, err := s.GetPath(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	conn, err := s.UpgradeRequest(w, r)
	if err != nil {
		http.Error(w, "Failed to upgrade to websocket", http.StatusInternalServerError)
		return
	}

	connection := NewConnection(r.Context(), conn, s.config.WriteMessageType, s.config.ReadTimeout, s.config.WriteTimeout)

	defer fmt.Println("ws reader connection closed")
	defer connection.Close()

	pipe.AddConnectionAndWaitUntilFatal(connection) // TODO: ACCESSING PIPE AFTER MUX
}

func (s *Server) AddPath(path string, pipe *Pipe) {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.paths[path] = pipe
}

func (s *Server) GetPath(path string) (*Pipe, error) {
	if err := s.ExistsPath(path); err == nil {
		return nil, errors.New("topic does not exists")
	}

	s.mux.RLock()
	defer s.mux.RUnlock()

	return s.paths[path], nil
}

func (s *Server) ExistsPath(path string) error {
	s.mux.RLock()
	defer s.mux.RUnlock()

	_, exists := s.paths[path]
	if exists {
		return errors.New("topic already exists")
	}

	return nil
}

func (s *Server) RemovePath(path string) {
	s.mux.Lock()
	defer s.mux.Unlock()

	delete(s.paths, path)
}

func (s *Server) Close() error {
	var err error = nil

	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}

		s.metrics = &metrics{}

		s.config = ServerConfig{}
		err = s.httpServer.Close()
	})

	return err
}

func GetPathVariable(r *http.Request, name string) (string, error) {
	value := r.PathValue(name)
	if value == "" {
		return "", errors.New("path value empty")
	}

	return value, nil
}
