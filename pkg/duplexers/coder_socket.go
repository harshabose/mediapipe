package duplexers

import (
	"context"
	"fmt"

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

func (r *Websocket) Ping(ctx context.Context) error {
	if r.conn == nil {
		return nil
	}

	return r.conn.Ping(ctx)
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

func (r *Websocket) Close() {
	if err := r.conn.Close(websocket.StatusNormalClosure, "terminated"); err != nil {
		fmt.Printf("Websocket: error while closing err=%v\n", err)
	}
}
