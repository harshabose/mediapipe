package https

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/time/rate"
)

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

func (s *Server) ServeAndWait() <-chan struct{} {
	s.Serve()
	return s.ctx.Done()
}

// start starts the prod with the given configuration and listens for clients.
// This is a blocking call and must be called in a separate goroutine.
func (s *Server) start() {
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
		if s.cancel != nil {
			s.cancel()
		}

		s.wg.Wait()

		s.mux.Lock()
		defer s.mux.Unlock()

		// Give the server 30 seconds to finish existing requests
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err = s.httpServer.Shutdown(ctx); err != nil {
			fmt.Printf("graceful shutdown not possible. Closing forcibily...")
			if err := s.httpServer.Close(); err != nil {
				fmt.Printf("error while closing http server: %v", err)
			}
		}
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
		// apiKey := r.Header.Get("X-Internal-API-Key")
		// if apiKey == "" {
		// 	http.Error(w, "Missing internal API key", http.StatusUnauthorized)
		// 	return
		// }
		//
		// if subtle.ConstantTimeCompare([]byte(apiKey), []byte(s.config.InternalAPIKey)) != 1 {
		// 	http.Error(w, "Invalid internal API key", http.StatusUnauthorized)
		// 	return
		// }
		//
		// clientIP := s.getClientIP(r)
		// if !s.isInternalIP(clientIP) {
		// 	log.Printf("⚠️  Internal API access from external IP: %s", clientIP)
		// }

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
		clientIP := s.getClientIP(r)

		var limit rate.Limit
		var burst int

		if isPublic {
			limit = rate.Limit(s.config.PublicRateLimit) / 60 // per second
			burst = s.config.BurstSize
		} else {
			limit = rate.Limit(s.config.InternalRateLimit) / 60 // per second
			burst = s.config.BurstSize * 2                      // Higher burst for internal
		}

		s.rateLimiterMux.Lock()
		limiter, exists := s.rateLimiters.Get(clientIP)
		if !exists {
			limiter = rate.NewLimiter(limit, burst)
			s.rateLimiters.Add(clientIP, limiter)
		}
		s.rateLimiterMux.Unlock()

		if !limiter.Allow() {
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(int(limit*60)))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))

			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(int(limit*60)))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatFloat(limiter.TokensAt(time.Now()), 'f', -1, 64))

		next.ServeHTTP(w, r)
	}
}

func (s *Server) CorsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		originAllowed := s.isOriginAllowed(origin)

		if r.Method == http.MethodOptions {
			if !s.handlePreflight(w, r, originAllowed) {
				return
			}
		} else {
			if !s.handleActualRequest(w, r, originAllowed) {
				return
			}
		}

		s.setCORSHeaders(w, origin, originAllowed)

		if r.Method == http.MethodOptions {
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

	// nil and * are treated the same
	if (s.config.AllowedOrigins == nil || slices.Contains(s.config.AllowedOrigins, "*")) && s.config.AllowWildcard {
		return true
	}

	for _, allowedOrigin := range s.config.AllowedOrigins {
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
	if !originAllowed {
		w.Header().Add("Vary", "Origin")
		return
	}

	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}

	if s.config.AllowedMethods != nil {
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(s.config.AllowedMethods, ", "))
	} else {
		w.Header().Set("Access-Control-Allow-Methods", "*")
	}
	if s.config.AllowedHeaders != nil {
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(s.config.AllowedHeaders, ", "))
	} else {
		w.Header().Set("Access-Control-Allow-Headers", "*")
	}

	if s.config.AllowCredentials && origin != "" {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	// Set cache duration
	maxAge := s.config.MaxAge
	w.Header().Set("Access-Control-Max-Age", strconv.Itoa(maxAge))

	if s.config.ExposedHeaders != nil && len(s.config.ExposedHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(s.config.ExposedHeaders, ", "))
	}

	w.Header().Add("Vary", "Origin")
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
}

func (s *Server) isMethodAllowed(method string) bool {
	if method == "" {
		return false
	}

	if s.config.AllowedMethods == nil {
		return true
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

	if s.config.AllowedHeaders == nil {
		return true
	}

	// Headers that are always allowed (CORS-safe-listed headers)
	safeHeaders := map[string]bool{
		"accept":           true,
		"accept-language":  true,
		"content-language": true,
		"content-type":     true,
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

	// Check X-Real-IP header (from Nginx/Traefik/etc)
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) isInternalIP(ip string) bool {
	if s.config.TrustedNetworks == nil {
		return true
	}

	for _, network := range s.config.TrustedNetworks {
		if _, ipNet, err := net.ParseCIDR(network); err == nil {
			if ipNet.Contains(net.ParseIP(ip)) {
				return true
			}
		}
	}

	if isLoopBack(ip) {
		return true
	}

	return false
}

type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.headerWritten {
		return
	}
	rw.headerWritten = true
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("responseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	if !rw.headerWritten {
		rw.WriteHeader(200)
	}
	return rw.ResponseWriter.Write(data)
}

func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := rw.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return errors.New("push not supported")
}
