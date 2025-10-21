package duplexers

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

type Websocket struct {
	conn        *websocket.Conn
	once        sync.Once
	messageType websocket.MessageType

	ctx    context.Context
	cancel context.CancelFunc
}

func NewWebSocket(ctx context.Context, conn *websocket.Conn, msgType websocket.MessageType) *Websocket {
	ctx2, cancel2 := context.WithCancel(ctx)

	return &Websocket{
		conn:        conn,
		messageType: msgType,
		ctx:         ctx2,
		cancel:      cancel2,
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

func (r *Websocket) Done() <-chan struct{} {
	return r.ctx.Done()
}

func (r *Websocket) Close() error {
	var err error
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		err = r.conn.Close(websocket.StatusNormalClosure, "terminated")
	})
	return err
}
