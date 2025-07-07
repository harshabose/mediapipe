package socket

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/mediasink"
)

type Connection struct {
	conn   *websocket.Conn
	reader *mediapipe.AnyReader[[]byte, []byte]
	writer *mediapipe.AnyWriter[[]byte, []byte]
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func NewConnection(ctx context.Context, conn *websocket.Conn, msgType websocket.MessageType, readTimeout time.Duration, writeTimeout time.Duration) *Connection {
	ctx2, cancel := context.WithCancel(ctx)

	rw := NewSocketReaderWriter(ctx2, conn, msgType, readTimeout, writeTimeout)

	c := &Connection{
		conn:   conn,
		reader: mediapipe.NewIdentityAnyReader[[]byte](rw),
		writer: mediapipe.NewIdentityAnyWriter[[]byte](rw),
		ctx:    ctx2,
		cancel: cancel,
	}

	return c
}

func (c *Connection) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			fmt.Println("connection cancel called")
			c.cancel()
		}
	})
}

type Pipe struct {
	readPipe    *mediapipe.FanoutPipe[[]byte, []byte]
	writePipe   *mediapipe.MergePipe[[]byte, []byte]
	owner       *Connection
	connections map[string]*Connection // Track all connected clients
	mux         sync.RWMutex
}

func NewPipe(owner *Connection) *Pipe {
	return &Pipe{
		readPipe:    mediapipe.NewFanoutPipe(owner.ctx, owner.reader),
		writePipe:   mediapipe.NewMergePipe(owner.ctx, owner.writer),
		owner:       owner,
		connections: make(map[string]*Connection),
	}
}

func (p *Pipe) WaitUntilFatal() {
	defer fmt.Println("fatal error occurred in WaitUntilFatal")
	select {
	case <-p.owner.ctx.Done():
		fmt.Println("owner ctx done")
		return
	case <-p.readPipe.Wait(): // TODO: DOES NOT RETURN WHEN READ ERROR OCCURS
		fmt.Println("read pipe ctx done")
		return
	case <-p.writePipe.Wait(): // TODO: DOES NOT RETURN WHEN WRITE ERROR OCCURS
		fmt.Println("write pipe ctx done")
		return
	}
}

func (p *Pipe) AddConnectionAndWaitUntilFatal(conn *Connection) {
	defer fmt.Println("fatal error occurred in AddConnectionAndWaitUntilFatal")
	wCtx, wCancel := p.readPipe.AddWriter(conn.writer)
	rCtx, rCancel := p.writePipe.AddReader(conn.reader)

	defer wCancel()
	defer rCancel()

	select {
	case <-conn.ctx.Done():
		fmt.Println("ws connection ctx done")
		return
	case <-p.owner.ctx.Done():
		fmt.Println("owner connection ctx done")
		return
	case <-wCtx.Done():
		fmt.Println("reader writer ctx done")
		return
	case <-rCtx.Done():
		fmt.Println("reader reader ctx done")
		return
	}
}
