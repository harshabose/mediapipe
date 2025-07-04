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

	AllowedRooms  []string `json:"allowed_rooms"`  // empty (not nil) means allow all
	AllowedTopics []string `json:"allowed_topics"` // empty (not nil) means allow all

	WriteMessageType websocket.MessageType `json:"write_message_type"`
	WriteBufferSize  uint32                `json:"write_buffer_size"`
	ReadBufferSize   uint32                `json:"read_buffer_size"`
}

type Server struct {
	id         string
	httpServer *https.Server

	config ServerConfig

	paths map[string]*Pipe

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc

	metrics *metrics
}

func NewServer(ctx context.Context, id string, config ServerConfig, httpsConfig https.Config, options ...ServerOption) (*Server, error) {
	ctx2, cancel := context.WithCancel(ctx)

	s := &Server{
		id:         id,
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

	s.httpServer.AddRequestHandler("/ws/write/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(s.wsWriteHandler, true))))
	s.httpServer.AddRequestHandler("/ws/read/{room}/{topic}", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(s.httpServer.RateLimitMiddleware(s.wsReadHandler, true))))

	s.httpServer.AddRequestHandler("GET /metrics", s.httpServer.LoggingMiddleware(s.httpServer.CorsMiddleware(
		s.httpServer.InternalAuthMiddleware(s.httpServer.RateLimitMiddleware(s.metricsHandler, false)))))

	for _, option := range options {
		if err := option(s); err != nil {
			return nil, fmt.Errorf("faild to apply option: %v", err)
		}
	}

	return s, nil
}

func (s *Server) Serve() {
	s.httpServer.Serve()
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

	conn, err := s.UpgradeRequest(w, r)
	if err != nil {
		http.Error(w, "Failed to upgrade to websocket", http.StatusInternalServerError)
		return
	}

	_, exists := s.paths[path]
	if exists {
		http.Error(w, "Resource already exists", http.StatusInternalServerError)
		return
	}

	connection, err := NewConnection(r.Context(), conn, s.config.WriteMessageType, s.config.ReadBufferSize, s.config.WriteBufferSize)
	if err != nil {
		http.Error(w, "Failed to allocate resources", http.StatusInternalServerError)
		return
	}

	defer delete(s.paths, path)
	s.paths[path] = NewPipe(connection)

	s.paths[path].WaitUntilFatal()
}

func (s *Server) wsReadHandler(w http.ResponseWriter, r *http.Request) {
	s.WsReadHandler(w, r)
}

func (s *Server) WsReadHandler(w http.ResponseWriter, r *http.Request) {
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

	conn, err := s.UpgradeRequest(w, r)
	if err != nil {
		http.Error(w, "Failed to upgrade to websocket", http.StatusInternalServerError)
		return
	}

	pipe, exists := s.paths[path]
	if !exists {
		http.Error(w, "Topic not found - writer must connect first", http.StatusNotFound)
		return
	}

	connection, err := NewConnection(r.Context(), conn, s.config.WriteMessageType, s.config.ReadBufferSize, s.config.WriteBufferSize)
	if err != nil {
		http.Error(w, "Failed to allocate resources", http.StatusInternalServerError)
		return
	}

	pipe.AddConnectionAndWaitUntilFatal(connection)
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
