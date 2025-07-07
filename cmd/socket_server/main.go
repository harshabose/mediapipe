package main

import (
	"context"
	"time"

	"github.com/harshabose/mediasink/pkg/https"
	"github.com/harshabose/mediasink/pkg/socket"
)

func main() {
	<-socket.NewServer(context.Background(), socket.DefaultServerConfig(), https.Config{
		Addr:              "127.0.0.1",
		Port:              8080,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		KeepHosting:       true,
		PublicRateLimit:   60,
		InternalRateLimit: 300,
		BurstSize:         10,
		AllowedOrigins:    []string{"*"},
		AllowedMethods:    []string{"GET"},
		AllowedHeaders: []string{
			"Accept",
			"Accept-Language",
			"Content-Language",
			"Content-Type",
			"Authorization",
			"X-Requested-With",
			"X-Internal-API-Key",
		},
		ExposedHeaders: []string{
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-RateLimit-Reset",
		},
		AllowCredentials: false,
		StrictMode:       false,
		AllowWildcard:    true,
		LogViolations:    true,
		MaxAge:           86400,
	}).StartAndWait()
}
