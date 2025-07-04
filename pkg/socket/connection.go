package socket

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/harshabose/mediasink"
	"github.com/harshabose/mediasink/pkg/ioreader"
	"github.com/harshabose/mediasink/pkg/iowriter"
)

type Connection struct {
	conn   *websocket.Conn
	reader *mediasink.AnyReader[[]byte, []byte]
	writer *mediasink.AnyWriter[[]byte, []byte]
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
}

func NewConnection(ctx context.Context, conn *websocket.Conn, msgType websocket.MessageType, readBufSize, writeBufSize uint32) (*Connection, error) {
	ctx2, cancel := context.WithCancel(ctx)

	_, r, err := conn.Reader(ctx2)
	if err != nil {
		cancel()
		return nil, err
	}

	reader, err := ioreader.NewReader(r, readBufSize)
	if err != nil {
		cancel()
		return nil, err
	}

	w, err := conn.Writer(ctx2, msgType)
	if err != nil {
		cancel()
		return nil, err
	}

	writer, err := iowriter.NewWriter(w, writeBufSize)
	if err != nil {
		cancel()
		return nil, err
	}

	c := &Connection{
		reader: mediasink.NewIdentityAnyReader[[]byte](reader),
		writer: mediasink.NewIdentityAnyWriter[[]byte](writer),
		ctx:    ctx2,
		cancel: cancel,
	}

	c.wg.Add(1)
	go c.ping()

	return c, nil
}

func (c *Connection) ping() {
	defer c.Close()
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.conn.Ping(c.ctx); err != nil {
				fmt.Printf("error while pinging to connection: %v", err)
				return
			}
		}
	}
}

func (c *Connection) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}

		c.wg.Wait()
	})
}

type Pipe struct {
	readPipe    *mediasink.FanoutPipe[[]byte, []byte]
	writePipe   *mediasink.MergePipe[[]byte, []byte]
	owner       *Connection
	connections map[string]*Connection // Track all connected clients
	mux         sync.RWMutex
}

func NewPipe(owner *Connection) *Pipe {
	return &Pipe{
		readPipe:    mediasink.NewFanoutPipe(owner.ctx, owner.reader),
		writePipe:   mediasink.NewMergePipe(owner.ctx, owner.writer),
		owner:       owner,
		connections: make(map[string]*Connection),
	}
}

func (p *Pipe) WaitUntilFatal() {
	select {
	case <-p.owner.ctx.Done():
		return
	case <-p.readPipe.Wait():
		return
	case <-p.writePipe.Wait():
		return
	}
}

func (p *Pipe) AddConnectionAndWaitUntilFatal(conn *Connection) {
	wCtx, wCancel := p.readPipe.AddWriter(conn.writer)
	rCtx, rCancel := p.writePipe.AddReader(conn.reader)

	defer wCancel()
	defer rCancel()

	select {
	case <-conn.ctx.Done():
		return
	case <-wCtx.Done():
		return
	case <-rCtx.Done():
		return
	}
}
