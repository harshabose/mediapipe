package duplexers

import (
	"context"
	"errors"
	"time"

	"github.com/coder/websocket"
)

type CoderWebsocket struct {
	conn         *websocket.Conn
	ctx          context.Context
	readTimeout  time.Duration
	writeTimeout time.Duration
	messageType  websocket.MessageType
}

func NewCoderSocket(ctx context.Context, conn *websocket.Conn, msgType websocket.MessageType, readTimeout time.Duration, writeTimeout time.Duration) *CoderWebsocket {
	return &CoderWebsocket{
		conn:         conn,
		ctx:          ctx,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		messageType:  msgType,
	}
}

func (r *CoderWebsocket) Generate() ([]byte, error) {
	ctx, cancel := context.WithTimeout(r.ctx, r.readTimeout)
	defer cancel()

	_, data, err := r.conn.Read(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, nil
	}
	return data, err
}

func (r *CoderWebsocket) Consume(data []byte) error {
	ctx, cancel := context.WithTimeout(r.ctx, r.writeTimeout)
	defer cancel()

	if err := r.conn.Write(ctx, r.messageType, data); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	return nil
}
