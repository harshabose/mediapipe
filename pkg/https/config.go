package https

import "time"

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

func DefaultConfig() Config {
	c := Config{}
	c.SetDefaults()

	return c
}

func (c *Config) SetDefaults() {
	if c.Addr == "" {
		c.Addr = "0.0.0.0"
	}

	if c.Port == 0 {
		c.Port = 8080
	}

	if c.ReadTimeout == 0 {
		c.ReadTimeout = 30 * time.Second
	}

	if c.WriteTimeout == 0 {
		c.WriteTimeout = 30 * time.Second
	}

	// KeepHosting defaults to false (zero value is appropriate)

	// Rate limiting defaults
	if c.PublicRateLimit == 0 {
		c.PublicRateLimit = 60 // 60 requests per minute
	}

	if c.InternalRateLimit == 0 {
		c.InternalRateLimit = 300 // 300 requests per minute
	}

	if c.BurstSize == 0 {
		c.BurstSize = 10
	}

	if len(c.AllowedOrigins) == 0 {
		c.AllowedOrigins = []string{"*"} // Allow all origins by default
	}

	if len(c.AllowedMethods) == 0 {
		c.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"}
	}

	if len(c.AllowedHeaders) == 0 { // Common Headers
		c.AllowedHeaders = []string{
			"Accept",
			"Accept-Language",
			"Content-Language",
			"Content-Type",
			"Authorization",
			"X-Requested-With",
			"X-Internal-API-Key",
		}
	}

	if len(c.ExposedHeaders) == 0 { // Exposed Headers; Can be added
		c.ExposedHeaders = []string{
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-RateLimit-Reset",
		}
	}

	// AllowCredentials defaults to false (zero value is appropriate)
	// StrictMode defaults to false (zero value is appropriate)

	if !c.AllowWildcard {
		c.AllowWildcard = true // Allow wildcard origins by default
	}

	// LogViolations defaults to false (zero value is appropriate)

	if c.MaxAge == 0 {
		c.MaxAge = 86400 // 24 hours cache for preflight requests
	}
}

func (c *Config) AddAllowedHeaders(headers ...string) {
	if c.AllowedHeaders == nil {
		return
	}

	c.AllowedHeaders = append(c.AllowedHeaders, headers...)
}

func (c *Config) AddTrustedNetworks(networks ...string) {
	if c.TrustedNetworks == nil {
		return
	}

	c.TrustedNetworks = append(c.TrustedNetworks, networks...)
}

func (c *Config) AddAllowedOrigins(origins ...string) {
	if c.AllowedOrigins == nil {
		return
	}

	c.AllowedOrigins = append(c.AllowedOrigins, origins...)
}

func (c *Config) AddAllowedMethods(methods ...string) {
	if c.AllowedMethods == nil {
		return
	}

	c.AllowedMethods = append(c.AllowedMethods, methods...)
}

func (c *Config) AddExposedHeaders(headers ...string) {
	if c.ExposedHeaders == nil {
		return
	}

	c.ExposedHeaders = append(c.ExposedHeaders, headers...)
}
