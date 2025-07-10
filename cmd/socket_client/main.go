package main

import (
	"context"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/mediapipe"
	"github.com/harshabose/mediapipe/pkg/loopback"
	"github.com/harshabose/mediapipe/pkg/socket"
)

func main() {
	// ASSUMING MAVPROXY IS STREAMING TO 8000
	l, err := loopback.NewLoopBack(context.Background(), "127.0.0.1:8000")
	if err != nil {
		panic(err)
	}

	rl := mediapipe.NewIdentityAnyReader[[]byte](l)
	wl := mediapipe.NewIdentityAnyWriter[[]byte](l)

	client := socket.NewClient(context.Background(), socket.ClientConfig{
		Addr:           "ws://localhost",
		Port:           8080,
		Path:           "/ws/write/desh/5gfpv",
		MessageType:    websocket.MessageBinary,
		ReadTimeout:    10 * time.Minute,
		WriteTimeout:   10 * time.Minute,
		KeepConnecting: true,
		MaxRetry:       10,
		ReconnectDelay: 3 * time.Second,
	})

	rc := mediapipe.NewIdentityAnyReader[[]byte](client)
	wc := mediapipe.NewIdentityAnyWriter[[]byte](client)

	client.Connect()

	time.Sleep(5 * time.Second)

	mediapipe.NewAnyPipe(context.Background(), rl, wc)
	mediapipe.NewAnyPipe(context.Background(), rc, wl)

	<-client.Wait()
}
