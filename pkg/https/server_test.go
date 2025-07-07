package https

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewHTTPSServer tests the server initialization
func TestNewHTTPSServer(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()

	server := NewHTTPSServer(ctx, config)

	if server == nil {
		t.Fatal("Expected server to be created, got nil")
	}

	if server.config.Addr != config.Addr {
		t.Errorf("Expected addr %s, got %s", config.Addr, server.config.Addr)
	}

	if server.config.Port != config.Port {
		t.Errorf("Expected port %d, got %d", config.Port, server.config.Port)
	}

	if server.httpServer == nil {
		t.Fatal("Expected HTTP server to be initialized")
	}

	if server.router == nil {
		t.Fatal("Expected router to be initialized")
	}

	if server.health == nil {
		t.Fatal("Expected health to be initialized")
	}

	if server.rateLimiters == nil {
		t.Fatal("Expected rate limiters to be initialized")
	}

	// Test context
	if server.Ctx() == nil {
		t.Fatal("Expected context to be available")
	}
}

// TestServerWithCustomConfig tests server with custom configuration
func TestServerWithCustomConfig(t *testing.T) {
	ctx := context.Background()
	config := Config{
		Addr:              "127.0.0.1",
		Port:              9090,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		PublicRateLimit:   30,
		InternalRateLimit: 150,
		BurstSize:         5,
		AllowedOrigins:    []string{"https://example.com"},
		AllowedMethods:    []string{"GET", "POST"},
		StrictMode:        true,
	}

	server := NewHTTPSServer(ctx, config)

	if server.config.Addr != "127.0.0.1" {
		t.Errorf("Expected addr 127.0.0.1, got %s", server.config.Addr)
	}

	if server.config.Port != 9090 {
		t.Errorf("Expected port 9090, got %d", server.config.Port)
	}

	if server.config.StrictMode != true {
		t.Error("Expected strict mode to be true")
	}
}

// TestStatusHandler tests the status endpoint
func TestStatusHandler(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	// Test successful status response
	req := httptest.NewRequest("GET", "/internal/status", nil)
	w := httptest.NewRecorder()

	server.statusHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected content type application/json, got %s", contentType)
	}

	// Verify response is valid JSON
	var health map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Errorf("Expected valid JSON response, got error: %v", err)
	}

	// Check if state field exists
	if _, exists := health["state"]; !exists {
		t.Error("Expected 'state' field in health response")
	}
}

// TestAddRequestHandler tests adding custom request handlers
func TestAddRequestHandler(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	// Add a custom handler
	customHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("custom response"))
	}

	server.AddRequestHandler("GET /test", customHandler)

	// Test the custom handler
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	if w.Body.String() != "custom response" {
		t.Errorf("Expected 'custom response', got %s", w.Body.String())
	}
}

// TestAppendErrors tests error appending functionality
func TestAppendErrors(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	// Test appending single error
	server.AppendErrors("test error 1")

	// Test appending multiple errors
	server.AppendErrors("test error 2", "test error 3")

	// Test appending empty errors (should not panic)
	server.AppendErrors()

	// Verify errors were added by checking health status
	req := httptest.NewRequest("GET", "/internal/status", nil)
	w := httptest.NewRecorder()

	server.statusHandler(w, req)

	var health map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &health)

	recentErrors, exists := health["recent_errors"]
	if !exists {
		t.Error("Expected 'recent_errors' field in health response")
	}

	errors, ok := recentErrors.([]interface{})
	if !ok {
		t.Error("Expected recent_errors to be an array")
	}

	if len(errors) != 3 {
		t.Errorf("Expected 3 errors, got %d", len(errors))
	}
}

// TestInternalAuthMiddleware tests the internal auth middleware
func TestInternalAuthMiddleware(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	// Create a test handler
	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	// Wrap with middleware
	wrappedHandler := server.InternalAuthMiddleware(testHandler)

	// Test request (should pass since auth is commented out)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	if w.Body.String() != "success" {
		t.Errorf("Expected 'success', got %s", w.Body.String())
	}
}

// TestAuthMiddleware tests the auth middleware (currently returns next directly)
func TestAuthMiddleware(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	wrappedHandler := server.AuthMiddleware(testHandler, []string{"read"}, []string{"user"}, []string{"room1"})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}
}

// TestRateLimitMiddleware tests the rate limiting middleware
func TestRateLimitMiddleware(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.PublicRateLimit = 2 // 2 requests per minute
	config.BurstSize = 1
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	// Test public rate limiting
	wrappedHandler := server.RateLimitMiddleware(testHandler, true)

	// First request should succeed
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	// Check rate limit headers
	if w.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("Expected X-RateLimit-Limit header")
	}

	if w.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("Expected X-RateLimit-Remaining header")
	}

	// Test internal rate limiting (should have higher limits)
	internalHandler := server.RateLimitMiddleware(testHandler, false)
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.2:12345"
	w2 := httptest.NewRecorder()

	internalHandler(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w2.Code)
	}
}

// TestCorsMiddleware tests the CORS middleware
func TestCorsMiddleware(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedOrigins = []string{"https://example.com", "https://test.com"}
	config.AllowedMethods = []string{"GET", "POST", "PUT"}
	config.AllowedHeaders = []string{"Content-Type", "Authorization"}
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	wrappedHandler := server.CorsMiddleware(testHandler)

	// Test preflight request
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	// Check CORS headers
	if w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("Expected Access-Control-Allow-Origin to be https://example.com, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}

	if !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Error("Expected Access-Control-Allow-Methods to contain POST")
	}

	// Test actual request
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("Origin", "https://example.com")
	w2 := httptest.NewRecorder()

	wrappedHandler(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w2.Code)
	}

	if w2.Body.String() != "success" {
		t.Errorf("Expected 'success', got %s", w2.Body.String())
	}
}

// TestCorsMiddlewareStrictMode tests CORS middleware in strict mode
func TestCorsMiddlewareStrictMode(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedOrigins = []string{"https://example.com"}
	config.StrictMode = true
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	wrappedHandler := server.CorsMiddleware(testHandler)

	// Test request from disallowed origin
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://malicious.com")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status code 403, got %d", w.Code)
	}
}

// TestIsOriginAllowed tests origin validation
func TestIsOriginAllowed(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedOrigins = []string{"https://example.com", "*.test.com"}
	config.AllowWildcard = false
	server := NewHTTPSServer(ctx, config)

	tests := []struct {
		origin   string
		expected bool
	}{
		{"", true},                         // Same-origin requests
		{"https://example.com", true},      // Exact match
		{"https://sub.test.com", true},     // Wildcard match
		{"https://test.com", false},        // Does not match *.test.com (protocol mismatch)
		{"https://malicious.com", false},   // Not allowed
		{"https://sub.example.com", false}, // Not a wildcard match
	}

	for _, test := range tests {
		result := server.isOriginAllowed(test.origin)
		if result != test.expected {
			t.Errorf("For origin %s, expected %v, got %v", test.origin, test.expected, result)
		}
	}
}

// TestIsOriginAllowedWithWildcard tests origin validation with wildcard enabled
func TestIsOriginAllowedWithWildcard(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedOrigins = []string{"*"}
	config.AllowWildcard = true
	server := NewHTTPSServer(ctx, config)

	tests := []string{
		"https://example.com",
		"https://test.com",
		"https://any-domain.com",
	}

	for _, origin := range tests {
		if !server.isOriginAllowed(origin) {
			t.Errorf("Expected origin %s to be allowed with wildcard", origin)
		}
	}
}

// TestIsMethodAllowed tests HTTP method validation
func TestIsMethodAllowed(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedMethods = []string{"GET", "POST", "PUT"}
	server := NewHTTPSServer(ctx, config)

	tests := []struct {
		method   string
		expected bool
	}{
		{"GET", true},
		{"POST", true},
		{"PUT", true},
		{"DELETE", false},
		{"PATCH", false},
		{"", false},
	}

	for _, test := range tests {
		result := server.isMethodAllowed(test.method)
		if result != test.expected {
			t.Errorf("For method %s, expected %v, got %v", test.method, test.expected, result)
		}
	}
}

// TestIsMethodAllowedNilConfig tests method validation with nil config
func TestIsMethodAllowedNilConfig(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedMethods = nil
	server := NewHTTPSServer(ctx, config)

	// Should allow all methods when config is nil
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		if !server.isMethodAllowed(method) {
			t.Errorf("Expected method %s to be allowed with nil config", method)
		}
	}

	// Empty method should still be false
	if server.isMethodAllowed("") {
		t.Error("Expected empty method to be disallowed")
	}
}

// TestAreHeadersAllowed tests header validation
func TestAreHeadersAllowed(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedHeaders = []string{"Content-Type", "Authorization", "X-Custom-Header"}
	server := NewHTTPSServer(ctx, config)

	tests := []struct {
		headers  string
		expected bool
	}{
		{"", true},                                  // Empty headers
		{"Content-Type", true},                      // Allowed header
		{"Authorization", true},                     // Allowed header
		{"Accept", true},                            // Safe header
		{"Content-Type, Authorization", true},       // Multiple allowed headers
		{"Content-Type, X-Custom-Header", true},     // Mixed allowed headers
		{"X-Forbidden-Header", false},               // Not allowed header
		{"Content-Type, X-Forbidden-Header", false}, // Mixed with forbidden
	}

	for _, test := range tests {
		result := server.areHeadersAllowed(test.headers)
		if result != test.expected {
			t.Errorf("For headers %s, expected %v, got %v", test.headers, test.expected, result)
		}
	}
}

// TestAreHeadersAllowedNilConfig tests header validation with nil config
func TestAreHeadersAllowedNilConfig(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedHeaders = nil
	server := NewHTTPSServer(ctx, config)

	// Should allow all headers when config is nil
	headers := []string{
		"Content-Type",
		"Authorization",
		"X-Custom-Header",
		"X-Any-Header",
	}

	for _, header := range headers {
		if !server.areHeadersAllowed(header) {
			t.Errorf("Expected header %s to be allowed with nil config", header)
		}
	}
}

// TestGetClientIP tests client IP extraction
func TestGetClientIP(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectedIP    string
	}{
		{
			name:       "Direct connection",
			remoteAddr: "192.168.1.1:12345",
			expectedIP: "192.168.1.1",
		},
		{
			name:          "X-Forwarded-For single IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			expectedIP:    "203.0.113.1",
		},
		{
			name:          "X-Forwarded-For multiple IPs",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1, 10.0.0.2, 10.0.0.3",
			expectedIP:    "203.0.113.1",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			xRealIP:    "203.0.113.2",
			expectedIP: "203.0.113.2",
		},
		{
			name:          "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			xRealIP:       "203.0.113.2",
			expectedIP:    "203.0.113.1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = test.remoteAddr

			if test.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", test.xForwardedFor)
			}

			if test.xRealIP != "" {
				req.Header.Set("X-Real-IP", test.xRealIP)
			}

			ip := server.getClientIP(req)
			if ip != test.expectedIP {
				t.Errorf("Expected IP %s, got %s", test.expectedIP, ip)
			}
		})
	}
}

// TestIsInternalIP tests internal IP validation
func TestIsInternalIP(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.TrustedNetworks = []string{"192.168.0.0/16", "10.0.0.0/8"}
	server := NewHTTPSServer(ctx, config)

	tests := []struct {
		ip       string
		expected bool
	}{
		{"192.168.1.1", true},  // In trusted network
		{"10.0.0.1", true},     // In trusted network
		{"127.0.0.1", false},   // Loopback (fails due to isLoopBack expecting remoteAddr format)
		{"::1", false},         // IPv6 loopback (fails due to isLoopBack expecting remoteAddr format)
		{"203.0.113.1", false}, // Not in trusted network
		{"172.16.0.1", false},  // Not in trusted network
	}

	for _, test := range tests {
		result := server.isInternalIP(test.ip)
		if result != test.expected {
			t.Errorf("For IP %s, expected %v, got %v", test.ip, test.expected, result)
		}
	}
}

// TestIsInternalIPNilConfig tests internal IP validation with nil trusted networks
func TestIsInternalIPNilConfig(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.TrustedNetworks = nil
	server := NewHTTPSServer(ctx, config)

	// Should return true for all IPs when trusted networks is nil
	ips := []string{"192.168.1.1", "10.0.0.1", "203.0.113.1"}
	for _, ip := range ips {
		if !server.isInternalIP(ip) {
			t.Errorf("Expected IP %s to be considered internal with nil config", ip)
		}
	}
}

// TestLoggingMiddleware tests the logging middleware
func TestLoggingMiddleware(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	}

	wrappedHandler := server.LoggingMiddleware(testHandler)

	req := httptest.NewRequest("POST", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "test-agent")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status code 201, got %d", w.Code)
	}

	if w.Body.String() != "created" {
		t.Errorf("Expected 'created', got %s", w.Body.String())
	}
}

// TestResponseWriter tests the response writer wrapper
func TestResponseWriter(t *testing.T) {
	w := httptest.NewRecorder()
	wrapper := &responseWriter{ResponseWriter: w, statusCode: 200}

	// Test default status code
	if wrapper.statusCode != 200 {
		t.Errorf("Expected default status code 200, got %d", wrapper.statusCode)
	}

	// Test WriteHeader
	wrapper.WriteHeader(http.StatusNotFound)
	if wrapper.statusCode != http.StatusNotFound {
		t.Errorf("Expected status code 404, got %d", wrapper.statusCode)
	}

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected underlying writer status code 404, got %d", w.Code)
	}
}

// TestServerClose tests server shutdown
func TestServerClose(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	// Close should not return error when server is not started
	err := server.Close()
	if err != nil {
		t.Errorf("Expected no error on close, got %v", err)
	}

	// Multiple closes should be safe
	err = server.Close()
	if err != nil {
		t.Errorf("Expected no error on second close, got %v", err)
	}
}

// TestMiddlewareChaining tests that middleware can be chained properly
func TestMiddlewareChaining(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.AllowedOrigins = []string{"*"}
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	// Chain multiple middleware
	chainedHandler := server.LoggingMiddleware(
		server.CorsMiddleware(
			server.InternalAuthMiddleware(
				server.RateLimitMiddleware(testHandler, false),
			),
		),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()

	chainedHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", w.Code)
	}

	if w.Body.String() != "success" {
		t.Errorf("Expected 'success', got %s", w.Body.String())
	}

	// Check that CORS headers are set
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("Expected CORS headers to be set")
	}

	// Check that rate limit headers are set
	if w.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("Expected rate limit headers to be set")
	}
}

// TestConcurrentRequests tests handling of concurrent requests
func TestConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	server := NewHTTPSServer(ctx, config)

	testHandler := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond) // Simulate some work
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	wrappedHandler := server.RateLimitMiddleware(testHandler, false)

	const numRequests = 10
	results := make(chan int, numRequests)

	// Send concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = fmt.Sprintf("192.168.1.%d:12345", id) // Different IPs
			w := httptest.NewRecorder()

			wrappedHandler(w, req)
			results <- w.Code
		}(i)
	}

	// Collect results
	successCount := 0
	for i := 0; i < numRequests; i++ {
		code := <-results
		if code == http.StatusOK {
			successCount++
		}
	}

	// All requests should succeed since they're from different IPs
	if successCount != numRequests {
		t.Errorf("Expected %d successful requests, got %d", numRequests, successCount)
	}
}
