package https

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/time/rate"
)

type ServerState string

const (
	ServerDown ServerState = "SERVER_OFFLINE"
	ServerUp   ServerState = "SERVER_ONLINE"
)

func isLoopBack(remoteAddr string) bool {
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

// BufferedErrors is a fixed-size buffer for storing error messages
// It will remove the oldest error when the buffer is full
type BufferedErrors struct {
	maxSize int
	errors  []string
	mux     sync.RWMutex
}

// NewBufferedErrors creates a new BufferedErrors with the specified maximum size
func NewBufferedErrors(maxSize int) *BufferedErrors {
	return &BufferedErrors{
		maxSize: maxSize,
		errors:  make([]string, 0, maxSize),
	}
}

// Add adds a new error to the buffer, removing the oldest one if the buffer is full
func (be *BufferedErrors) Add(err string) {
	be.mux.Lock()
	defer be.mux.Unlock()

	if len(be.errors) >= be.maxSize {
		// Remove the oldest error (first element)
		be.errors = be.errors[1:]
	}

	// Add the new error
	be.errors = append(be.errors, err)
}

// MarshalJSON implements the json.Marshaler interface
func (be *BufferedErrors) MarshalJSON() ([]byte, error) {
	be.mux.RLock()
	defer be.mux.RUnlock()

	return json.Marshal(be.errors)
}

type health struct {
	State        ServerState     `json:"state"`
	RecentErrors *BufferedErrors `json:"recent_errors"`
	mux          sync.RWMutex
}

func (h *health) SetState(state ServerState) {
	h.mux.Lock()
	defer h.mux.Unlock()

	h.State = state
}

func (h *health) Marshal() ([]byte, error) {
	h.mux.RLock()
	defer h.mux.RUnlock()

	return json.Marshal(h)
}

// AddError adds an error message to the RecentErrors buffer
func (h *health) AddError(err string) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if h.RecentErrors != nil {
		h.RecentErrors.Add(err)
	}
}

type Config struct {
	Addr         string        `json:"addr"`
	Port         uint16        `json:"port"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	KeepHosting  bool          `json:"keep_hosting"`

	InternalAPIKey  string   `json:"-"`
	CertPath        string   `json:"-"`
	KeyFile         string   `json:"-"`
	TrustedNetworks []string `json:"-"`

	// Rate limiting configuration
	PublicRateLimit   int `json:"public_rate_limit"`   // requests per minute
	InternalRateLimit int `json:"internal_rate_limit"` // requests per minute
	BurstSize         int `json:"burst_size"`

	// CORS configuration
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	AllowedHeaders   []string `json:"allowed_headers"`
	ExposedHeaders   []string `json:"exposed_headers"` // Headers browsers can access
	AllowCredentials bool     `json:"allow_credentials"`

	// Security options
	StrictMode    bool `json:"strict_mode"`    // Enforce strict CORS validation
	AllowWildcard bool `json:"allow_wildcard"` // Allow "*" origin
	LogViolations bool `json:"log_violations"` // Log CORS violations
	MaxAge        int  `json:"max_age"`        // Cache duration for preflight
}

type Server struct {
	// auth       *auth.Client
	httpServer *http.Server
	config     Config
	router     *http.ServeMux

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	mux    sync.RWMutex
	wg     sync.WaitGroup

	rateLimiters   *expirable.LRU[string, *rate.Limiter]
	rateLimiterMux sync.RWMutex

	health *health
}

func NewHTTPSServer(ctx context.Context, config Config) *Server {
	ctx2, cancel := context.WithCancel(ctx)
	router := http.NewServeMux()

	s := &Server{
		router: router,
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.Addr, config.Port),
			ReadHeaderTimeout: config.ReadTimeout,
			WriteTimeout:      config.WriteTimeout,
			Handler:           router,
		},
		config: config,
		health: &health{
			RecentErrors: NewBufferedErrors(10), // Default to storing 10 most recent errors
		},
		rateLimiters: expirable.NewLRU[string, *rate.Limiter](10_000, nil, time.Hour),
		ctx:          ctx2,
		cancel:       cancel,
	}

	router.HandleFunc("GET /internal/status", s.LoggingMiddleware(s.CorsMiddleware(s.InternalAuthMiddleware(s.RateLimitMiddleware(s.statusHandler, false)))))

	return s
}

func (s *Server) Ctx() context.Context {
	return s.ctx
}

func (s *Server) AddRequestHandler(path string, handler http.HandlerFunc) {
	s.router.HandleFunc(path, handler)
}

func (s *Server) AppendErrors(err ...string) {
	if len(err) == 0 {
		return
	}

	for _, e := range err {
		s.health.AddError(e)
	}
}

func (s *Server) Serve() {
	s.wg.Add(1)
	go s.start()
}

// start starts the prod with the given configuration and listens for clients.
// This is a blocking call and must be called in a separate goroutine.
func (s *Server) start() {
	defer s.Close() // if parent ctx of s.ctx is cancelled
	defer s.wg.Done()
	defer s.health.SetState(ServerDown) // Ensure state is set on exit

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			s.health.SetState(ServerUp)

			var err error
			if s.config.CertPath != "" && s.config.KeyFile != "" {
				err = s.httpServer.ListenAndServeTLS(s.config.CertPath, s.config.KeyFile)
			} else {
				err = s.httpServer.ListenAndServe()
			}

			// Only set down state if there was an actual error
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.health.SetState(ServerDown)
				errMsg := fmt.Sprintf("error while serving: %s", err.Error())
				fmt.Println(errMsg)
				s.health.AddError(errMsg)

				if !s.config.KeepHosting {
					return
				}

				fmt.Println("failed to host server, retrying in 5 seconds...")
				time.Sleep(5 * time.Second)
			} else {
				// Graceful shutdown
				return
			}
		}
	}
}

// GET /internal/status
func (s *Server) statusHandler(w http.ResponseWriter, r *http.Request) {
	s.mux.RLock()

	msg, err := s.health.Marshal()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to marshal health status: %s", err.Error())
		s.health.AddError(errMsg)
		http.Error(w, "Failed to marshal health status", http.StatusInternalServerError)
		return
	}

	s.mux.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(msg); err != nil {
		errMsg := fmt.Sprintf("Error while sending status response: %s", err.Error())
		fmt.Println(errMsg)
		s.health.AddError(errMsg)
		return
	}
}

func (s *Server) Close() error {
	var err error = nil

	s.once.Do(func() {
		s.mux.Lock()
		defer s.mux.Unlock()

		if s.cancel != nil {
			s.cancel()
		}

		// Give the server 30 seconds to finish existing requests
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err = s.httpServer.Shutdown(ctx); err != nil {
			fmt.Printf("graceful shutdown not possible. Closing forcibily...")
			if err := s.httpServer.Close(); err != nil {
				fmt.Printf("error while closing http server: %v", err)
			}
		}

		s.wg.Wait()
	})

	return err
}

// ==========================
// MIDDLEWARE
// ==========================

// InternalAuthMiddleware Internal authentication middleware
func (s *Server) InternalAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check API key
		apiKey := r.Header.Get("X-Internal-API-Key")
		if apiKey == "" {
			http.Error(w, "Missing internal API key", http.StatusUnauthorized)
			return
		}

		// Use constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(apiKey), []byte(s.config.InternalAPIKey)) != 1 {
			http.Error(w, "Invalid internal API key", http.StatusUnauthorized)
			return
		}

		// Optional: Check if request comes from trusted network
		clientIP := s.getClientIP(r)
		if !s.isInternalIP(clientIP) {
			log.Printf("⚠️  Internal API access from external IP: %s", clientIP)
		}

		next.ServeHTTP(w, r)
	}
}

func (s *Server) AuthMiddleware(next http.HandlerFunc, requiredScope, requiredRoles, requiredRooms []string) http.HandlerFunc {
	return next
	// TODO: NOT IMPLEMENTED AS OF NOW
	// return s.auth.AuthMiddleware(next, requiredScope, requiredRoles, requiredRooms)
}

func (s *Server) AuthMiddlewareWithRequiredRoomExtraction(next http.HandlerFunc, requiredScope, requiredRoles []string, extractor func(r *http.Request) ([]string, error)) http.HandlerFunc {
	return next
	// TODO: NOT IMPLEMENT AS OF NOW
	// return s.auth.AuthMiddlewareWithRequiredRoomExtraction(next, requiredScope, requiredRoles, extractor)
}

// RateLimitMiddleware Rate limiting middleware
func (s *Server) RateLimitMiddleware(next http.HandlerFunc, isPublic bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get client identifier (IP address)
		clientIP := s.getClientIP(r)

		// Determine rate limit
		var limit rate.Limit
		var burst int

		if isPublic {
			limit = rate.Limit(s.config.PublicRateLimit) / 60 // per second
			burst = s.config.BurstSize
		} else {
			limit = rate.Limit(s.config.InternalRateLimit) / 60 // per second
			burst = s.config.BurstSize * 2                      // Higher burst for internal
		}

		// Get or create rate limiter for this client
		s.rateLimiterMux.Lock()
		limiter, exists := s.rateLimiters.Get(clientIP)
		if !exists {
			limiter = rate.NewLimiter(limit, burst)
			s.rateLimiters.Add(clientIP, limiter)
		}
		s.rateLimiterMux.Unlock()

		// Check rate limit
		if !limiter.Allow() {
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(int(limit*60)))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))

			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		// Add rate limit headers
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(int(limit*60)))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatFloat(limiter.TokensAt(time.Now()), 'f', -1, 64))

		next.ServeHTTP(w, r)
	}
}

func (s *Server) CorsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Check origin
		originAllowed := s.isOriginAllowed(origin)

		if r.Method == "OPTIONS" {
			// Handle preflight
			if !s.handlePreflight(w, r, originAllowed) {
				return
			}
		} else {
			// Handle actual request
			if !s.handleActualRequest(w, r, originAllowed) {
				return
			}
		}

		// Set CORS headers
		s.setCORSHeaders(w, origin, originAllowed)

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	}
}

func (s *Server) isOriginAllowed(origin string) bool {
	if origin == "" {
		return true // Same-origin requests
	}

	for _, allowedOrigin := range s.config.AllowedOrigins {
		if allowedOrigin == "*" && s.config.AllowWildcard {
			return true
		}
		if allowedOrigin == origin {
			return true
		}

		if strings.HasPrefix(allowedOrigin, "*.") {
			domain := allowedOrigin[2:] // Remove "*."
			if strings.HasSuffix(origin, "."+domain) || origin == domain {
				return true
			}
		}
	}
	return false
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request, originAllowed bool) bool {
	requestedMethod := r.Header.Get("Access-Control-Request-Method")
	requestedHeaders := r.Header.Get("Access-Control-Request-Headers")

	if s.config.StrictMode {
		if !originAllowed {
			s.logCORSViolation("origin not allowed", r)
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return false
		}

		if !s.isMethodAllowed(requestedMethod) {
			s.logCORSViolation("method not allowed", r)
			http.Error(w, "Method not allowed", http.StatusForbidden)
			return false
		}

		if !s.areHeadersAllowed(requestedHeaders) {
			s.logCORSViolation("headers not allowed", r)
			http.Error(w, "Headers not allowed", http.StatusForbidden)
			return false
		}
	}

	return true
}

func (s *Server) handleActualRequest(w http.ResponseWriter, r *http.Request, originAllowed bool) bool {
	if s.config.StrictMode {
		if !originAllowed {
			s.logCORSViolation("origin not allowed for actual request", r)
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return false
		}

		if !s.isMethodAllowed(r.Method) {
			s.logCORSViolation("method not allowed for actual request", r)
			http.Error(w, "Method not allowed by CORS", http.StatusMethodNotAllowed)
			return false
		}
	}

	return true
}

func (s *Server) setCORSHeaders(w http.ResponseWriter, origin string, originAllowed bool) {
	if originAllowed {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
	}

	w.Header().Set("Access-Control-Allow-Methods", strings.Join(s.config.AllowedMethods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(s.config.AllowedHeaders, ", "))

	if s.config.AllowCredentials && originAllowed && origin != "" {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	// Set cache duration
	maxAge := s.config.MaxAge
	if maxAge == 0 {
		maxAge = 86400 // Default 24 hours
	}
	w.Header().Set("Access-Control-Max-Age", strconv.Itoa(maxAge))

	// Set exposed headers (headers that browser JS can access)
	if len(s.config.ExposedHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(s.config.ExposedHeaders, ", "))
	}

	// Vary headers for proper caching
	w.Header().Add("Vary", "Origin")
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
}

func (s *Server) isMethodAllowed(method string) bool {
	if method == "" {
		return false
	}

	for _, allowedMethod := range s.config.AllowedMethods {
		if allowedMethod == method {
			return true
		}
	}
	return false
}

func (s *Server) areHeadersAllowed(requestedHeaders string) bool {
	if requestedHeaders == "" {
		return true
	}

	// Headers that are always allowed (CORS-safe-listed headers)
	safeHeaders := map[string]bool{
		"accept":           true,
		"accept-language":  true,
		"content-language": true,
		"content-type":     true, // with restrictions, but we'll allow it
	}

	headers := strings.Split(requestedHeaders, ",")

	for _, header := range headers {
		header = strings.TrimSpace(strings.ToLower(header))

		if header == "" {
			continue
		}

		if safeHeaders[header] {
			continue
		}

		headerAllowed := false
		for _, allowedHeader := range s.config.AllowedHeaders {
			if strings.ToLower(strings.TrimSpace(allowedHeader)) == header {
				headerAllowed = true
				break
			}
		}

		if !headerAllowed {
			return false
		}
	}

	return true
}

func (s *Server) logCORSViolation(reason string, r *http.Request) {
	if s.config.LogViolations {
		log.Printf("CORS violation: %s - Origin: %s, Method: %s, Headers: %s",
			reason,
			r.Header.Get("Origin"),
			r.Header.Get("Access-Control-Request-Method"),
			r.Header.Get("Access-Control-Request-Headers"))
	}
}

// LoggingMiddleware Logging middleware
func (s *Server) LoggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrapper := &responseWriter{ResponseWriter: w, statusCode: 200}

		next.ServeHTTP(wrapper, r)

		duration := time.Since(start)
		clientIP := s.getClientIP(r)

		log.Printf("%s %s %s %d %v %s",
			r.Method,
			r.URL.Path,
			clientIP,
			wrapper.statusCode,
			duration,
			r.UserAgent(),
		)
	}
}

// ==========================
// HELPER FUNCTIONS
// ==========================

func (s *Server) getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (from load balancers/proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (original client)
		if ips := strings.Split(xff, ","); len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header (from Nginx)
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) isInternalIP(ip string) bool {
	// Check if IP is in trusted networks
	for _, network := range s.config.TrustedNetworks {
		if _, ipNet, err := net.ParseCIDR(network); err == nil {
			if ipNet.Contains(net.ParseIP(ip)) {
				return true
			}
		}
	}

	// Check for localhost
	if isLoopBack(ip) {
		return true
	}

	return false
}

// Response writer wrapper to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
