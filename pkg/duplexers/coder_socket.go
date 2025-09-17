package duplexers

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

type Websocket struct {
	conn        *websocket.Conn
	once        sync.Once
	ctx         context.Context
	messageType websocket.MessageType
}

func NewWebSocket(ctx context.Context, conn *websocket.Conn, msgType websocket.MessageType) *Websocket {
	return &Websocket{
		conn:        conn,
		ctx:         ctx,
		messageType: msgType,
	}
}

func (r *Websocket) Generate() ([]byte, error) {
	_, data, err := r.conn.Read(r.ctx)
	return data, err
}

func (r *Websocket) Consume(data []byte) error {
	if r.conn == nil {
		return nil
	}

	if err := r.conn.Write(r.ctx, r.messageType, data); err != nil {
		return err
	}
	return nil
}

func (r *Websocket) Close() error {
	var err error
	r.once.Do(func() {
		err = r.conn.Close(websocket.StatusNormalClosure, "terminated")
	})
	return err
}
