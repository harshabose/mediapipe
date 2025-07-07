package socket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/mediasink/pkg/https"
)

func TestMetrics(t *testing.T) {
	m := &metrics{}
	m.resetUptime()

	// Test initial values
	if m.active() != 0 {
		t.Errorf("Expected active connections to be 0, got %d", m.active())
	}
	if m.failed() != 0 {
		t.Errorf("Expected failed connections to be 0, got %d", m.failed())
	}

	// Test increasing active connections
	m.increaseActiveConnections()
	if m.active() != 1 {
		t.Errorf("Expected active connections to be 1, got %d", m.active())
	}

	// Test increasing failed connections
	m.increaseFailedConnections()
	if m.failed() != 1 {
		t.Errorf("Expected failed connections to be 1, got %d", m.failed())
	}

	// Test decreasing active connections
	m.decreaseActiveConnections()
	if m.active() != 0 {
		t.Errorf("Expected active connections to be 0, got %d", m.active())
	}

	// Test data tracking
	m.addDataSent(100)
	m.addDataRecvd(200)
	if m.TotalDataSent != 100 {
		t.Errorf("Expected total data sent to be 100, got %d", m.TotalDataSent)
	}
	if m.TotalDataRecvd != 200 {
		t.Errorf("Expected total data received to be 200, got %d", m.TotalDataRecvd)
	}

	// Test uptime update
	time.Sleep(10 * time.Millisecond)
	m.updateUptime()
	if m.Uptime <= 0 {
		t.Errorf("Expected uptime to be positive, got %v", m.Uptime)
	}

	// Test marshaling
	data, err := m.Marshal()
	if err != nil {
		t.Errorf("Expected no error marshaling metrics, got %v", err)
	}
	if len(data) == 0 {
		t.Error("Expected marshaled data to be non-empty")
	}
}

func TestDefaultServerConfig(t *testing.T) {
	config := DefaultServerConfig()

	// Test default values
	if config.TotalConnections != 100 {
		t.Errorf("Expected default total connections to be 100, got %d", config.TotalConnections)
	}
	if len(config.WriteRequiredScope) == 0 {
		t.Error("Expected default write required scope to be set")
	}
	if len(config.WriteRequiredRoles) == 0 {
		t.Error("Expected default write required roles to be set")
	}
	if len(config.ReadRequiredScope) == 0 {
		t.Error("Expected default read required scope to be set")
	}
	if len(config.ReadRequiredRoles) == 0 {
		t.Error("Expected default read required roles to be set")
	}
	if config.WriteMessageType != websocket.MessageBinary {
		t.Errorf("Expected default write message type to be binary, got %v", config.WriteMessageType)
	}
	if config.WriteTimeout != 4096 {
		t.Errorf("Expected default write buffer size to be 4096, got %d", config.WriteTimeout)
	}
	if config.ReadTimeout != 4096 {
		t.Errorf("Expected default read buffer size to be 4096, got %d", config.ReadTimeout)
	}
}

func TestServerConfigSetDefaults(t *testing.T) {
	config := ServerConfig{}
	config.SetDefaults()

	// Test that defaults are set correctly
	if config.TotalConnections != 100 {
		t.Errorf("Expected total connections to be 100, got %d", config.TotalConnections)
	}

	// Test that existing values are not overridden
	config2 := ServerConfig{
		TotalConnections: 50,
		WriteTimeout:     2048,
	}
	config2.SetDefaults()

	if config2.TotalConnections != 50 {
		t.Errorf("Expected total connections to remain 50, got %d", config2.TotalConnections)
	}
	if config2.WriteTimeout != 2048 {
		t.Errorf("Expected write buffer size to remain 2048, got %d", config2.WriteTimeout)
	}
}

func TestNewServer(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)

	if server == nil {
		t.Fatal("Expected server to be created")
	}
	if server.config.TotalConnections != config.TotalConnections {
		t.Errorf("Expected server config to match input config")
	}
	if server.metrics == nil {
		t.Error("Expected server metrics to be initialized")
	}
	if server.paths == nil {
		t.Error("Expected server paths to be initialized")
	}
	if server.httpServer == nil {
		t.Error("Expected HTTP server to be initialized")
	}
}

func TestServerClose(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)
	err := server.Close()

	if err != nil {
		t.Errorf("Expected no error closing server, got %v", err)
	}

	// Test that close is idempotent
	err2 := server.Close()
	if err2 != nil {
		t.Errorf("Expected no error on second close, got %v", err2)
	}
}

func TestGetPathVariable(t *testing.T) {
	tests := []struct {
		name        string
		pathValue   string
		paramName   string
		expectError bool
		expected    string
	}{
		{
			name:        "valid path variable",
			pathValue:   "test-room",
			paramName:   "room",
			expectError: false,
			expected:    "test-room",
		},
		{
			name:        "empty path variable",
			pathValue:   "",
			paramName:   "room",
			expectError: true,
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.SetPathValue(tt.paramName, tt.pathValue)

			result, err := GetPathVariable(req, tt.paramName)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestUpgradeRequest(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	config.TotalConnections = 1 // Set low limit for testing
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)

	// Test connection limit
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	// First connection should work (but will fail due to test environment)
	_, err := server.UpgradeRequest(w, req)
	// We expect an error here because we're not in a real WebSocket environment
	if err == nil {
		t.Error("Expected error due to test environment")
	}

	// Simulate reaching connection limit
	server.metrics.ActiveConnections = config.TotalConnections
	_, err = server.UpgradeRequest(w, req)
	if err == nil {
		t.Error("Expected error when connection limit reached")
	}
	if !strings.Contains(err.Error(), "max clients reached") {
		t.Errorf("Expected 'max clients reached' error, got %v", err)
	}
}

func TestMetricsHandler(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)
	server.metrics.resetUptime()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)

	server.metricsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected content type application/json, got %s", contentType)
	}

	// Verify response is valid JSON
	var response json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Errorf("Expected valid JSON response, got error: %v", err)
	}
}

func TestWsWriteHandlerValidation(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	config.AllowedRooms = []string{"allowed-room"}
	config.AllowedTopics = []string{"allowed-topic"}
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)

	tests := []struct {
		name           string
		room           string
		topic          string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "missing room parameter",
			room:           "",
			topic:          "test-topic",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid room path parameter",
		},
		{
			name:           "missing topic parameter with allowed room",
			room:           "allowed-room",
			topic:          "",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "topic parameter required",
		},
		{
			name:           "room not allowed",
			room:           "forbidden-room",
			topic:          "allowed-topic",
			expectedStatus: http.StatusForbidden,
			expectedError:  "room not allowed",
		},
		{
			name:           "topic not allowed",
			room:           "allowed-room",
			topic:          "forbidden-topic",
			expectedStatus: http.StatusForbidden,
			expectedError:  "topic not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/ws/write/"+tt.room+"/"+tt.topic, nil)
			req.SetPathValue("room", tt.room)
			req.SetPathValue("topic", tt.topic)

			server.WsWriteHandler(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.expectedError != "" && !strings.Contains(w.Body.String(), tt.expectedError) {
				t.Errorf("Expected error message to contain '%s', got '%s'", tt.expectedError, w.Body.String())
			}
		})
	}
}

func TestWsReadHandlerValidation(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)

	tests := []struct {
		name           string
		room           string
		topic          string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "missing room parameter",
			room:           "",
			topic:          "test-topic",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid room path parameter",
		},
		{
			name:           "missing topic parameter",
			room:           "test-room",
			topic:          "",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "topic parameter required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/ws/read/"+tt.room+"/"+tt.topic, nil)
			req.SetPathValue("room", tt.room)
			req.SetPathValue("topic", tt.topic)

			server.WsReadHandler(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.expectedError != "" && !strings.Contains(w.Body.String(), tt.expectedError) {
				t.Errorf("Expected error message to contain '%s', got '%s'", tt.expectedError, w.Body.String())
			}
		})
	}
}

func TestWsReadHandlerTopicNotFound(t *testing.T) {
	ctx := context.Background()
	config := DefaultServerConfig()
	httpsConfig := https.DefaultConfig()

	server := NewServer(ctx, config, httpsConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws/read/test-room/test-topic", nil)
	req.SetPathValue("room", "test-room")
	req.SetPathValue("topic", "test-topic")

	// Add proper WebSocket headers
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	server.WsReadHandler(w, req)

	// The WebSocket upgrade will fail in test environment, but we can check that
	// it gets past the room/topic validation and attempts the upgrade
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "invalid room path parameter") {
		t.Error("Should have passed room validation")
	}
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "topic parameter required") {
		t.Error("Should have passed topic validation")
	}
}

func TestServerConfigAllowedRoomsAndTopics(t *testing.T) {
	ctx := context.Background()

	// Test with nil allowed rooms/topics (should allow all)
	config1 := DefaultServerConfig()
	config1.AllowedRooms = nil
	config1.AllowedTopics = nil
	httpsConfig := https.DefaultConfig()

	server1 := NewServer(ctx, config1, httpsConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws/write/any-room/any-topic", nil)
	req.SetPathValue("room", "any-room")
	req.SetPathValue("topic", "any-topic")

	server1.WsWriteHandler(w, req)

	// Should not get forbidden errors for room/topic validation
	if w.Code == http.StatusForbidden {
		t.Error("Expected rooms and topics to be allowed when config is nil")
	}

	// Test with empty slices (should still allow all due to SetDefaults)
	config2 := ServerConfig{}
	config2.SetDefaults()

	if config2.AllowedRooms != nil {
		t.Error("Expected AllowedRooms to be nil after SetDefaults")
	}
	if config2.AllowedTopics != nil {
		t.Error("Expected AllowedTopics to be nil after SetDefaults")
	}
}

func TestConcurrentMetricsAccess(t *testing.T) {
	m := &metrics{}
	m.resetUptime()

	// Test concurrent access to metrics
	done := make(chan bool)

	// Start multiple goroutines that modify metrics
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.increaseActiveConnections()
				m.addDataSent(10)
				m.addDataRecvd(20)
				m.updateUptime()
				m.decreaseActiveConnections()
				m.increaseFailedConnections()
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify metrics are in a consistent state
	if m.failed() != 1000 {
		t.Errorf("Expected 1000 failed connections, got %d", m.failed())
	}
	if m.TotalDataSent != 10000 {
		t.Errorf("Expected 10000 total data sent, got %d", m.TotalDataSent)
	}
	if m.TotalDataRecvd != 20000 {
		t.Errorf("Expected 20000 total data received, got %d", m.TotalDataRecvd)
	}
}
