package duplexers

import (
	"context"

	"github.com/coder/websocket"
)

type Websocket struct {
	conn        *websocket.Conn
	messageType websocket.MessageType
}

func NewWebSocket(conn *websocket.Conn, msgType websocket.MessageType) *Websocket {
	return &Websocket{
		conn:        conn,
		messageType: msgType,
	}
}

func (r *Websocket) Generate(ctx context.Context) ([]byte, error) {
	_, data, err := r.conn.Read(ctx)
	return data, err
}

func (r *Websocket) Consume(ctx context.Context, data []byte) error {
	if r.conn == nil {
		return nil
	}

	if err := r.conn.Write(ctx, r.messageType, data); err != nil {
		return err
	}
	return nil
}

func (r *Websocket) Close() error {
	if err := r.conn.Close(websocket.StatusNormalClosure, "terminated"); err != nil {
		return err
	}

	return nil
}
