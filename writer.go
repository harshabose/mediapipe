// Writer System - The Output Side of the Universal Media Router
//
// This file implements the writeAll/output side of the universal media routing system,
// providing the symmetric counterpart to the Reader system. While Readers handle
// data ingestion and transformation, Writers handle data consumption and output
// to various destinations.
//
// # The Writer Pattern
//
// The Writer system follows the same lazy transformation philosophy as the Reader
// system but in reverse - it takes Data[D, T] elements from the pipeline and
// transforms them for consumption by various output destinations:
//
//   - WebRTC Data → RTP packets → UDP sockets
//   - MAVLINK Data → WebSocket frames → Mission Planner
//   - Audio Data → compressed streams → RTSP servers
//   - Any Data → any format → any destination
//
// # Bidirectional Architecture
//
// Combined with the Reader system, this enables complete bidirectional pipelines:
//
//   Input: CanGenerate → Reader → BufferedReader → Data[D,T]
//   Output: Data[D,T] → BufferedWriter → Writer → CanConsume
//
// This allows building complex media routing topologies where any source can
// feed any destination through the same unified pipeline.

package mediapipe

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/buffer"
)

type Writer[D, T any] interface {
	Write(*Data[D, T]) error
	io.Closer
}

type CanConsume[D any] interface {
	Consume(D) error
}

type AnyWriter[D, T any] struct {
	consumer    CanConsume[D]      // The destination that will consume the data
	transformer func(D) (T, error) // Optional transformer for future use
}

func NewAnyWriter[D, T any](consumer CanConsume[D], transformer func(D) (T, error)) *AnyWriter[D, T] {
	return &AnyWriter[D, T]{
		consumer:    consumer,
		transformer: transformer,
	}
}

func NewIdentityAnyWriter[D any](consumer CanConsume[D]) *AnyWriter[D, D] {
	return &AnyWriter[D, D]{
		consumer: consumer,
		transformer: func(d D) (D, error) {
			return d, nil
		},
	}
}

func (w *AnyWriter[D, T]) Write(data *Data[D, T]) error {
	if data == nil {
		return nil
	}

	d, err := data.Get()
	if err != nil {
		return err
	}

	return w.consumer.Consume(d)
}

func (w *AnyWriter[D, T]) Close() error {
	return nil
}

type BufferedWriter[D, T any] struct {
	writer Writer[D, T]               // Direct reference to the underlying writer
	buffer buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background writing loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

func NewBufferedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize int) *BufferedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedWriter[D, T]{
		writer: writer,
		buffer: buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		ctx:    ctx2,
		cancel: cancel,
	}

	w.wg.Add(1)
	go w.loop()

	return w
}

func (w *BufferedWriter[D, T]) Write(data *Data[D, T]) error {
	return w.buffer.Push(w.ctx, data)
}

func (w *BufferedWriter[D, T]) loop() {
	defer w.Close()
	defer w.wg.Done()

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			data, err := w.buffer.Pop(w.ctx)
			if err != nil {
				fmt.Printf("buffered writer error while popping from buffer; err: %v", err)
				return
			}

			if err := w.writer.Write(data); err != nil {
				fmt.Printf("buffered writer error while writing to the writer; err: %v", err)
				return
			}
		}
	}
}

func (w *BufferedWriter[D, T]) Close() error {
	var err error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		if w.writer != nil {
			if err = w.writer.Close(); err != nil {
				return
			}
		}

		w.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}

type BufferedTimedWriter[D, T any] struct {
	writer   Writer[D, T]               // Direct reference to the underlying writer
	buffer   buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements
	interval time.Duration              // Time interval between writeAll operations

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background writing loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

func NewBufferedTimedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize int, interval time.Duration) *BufferedTimedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedTimedWriter[D, T]{
		writer:   writer,
		buffer:   buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	w.wg.Add(1)
	go w.loop()

	return w
}

func (w *BufferedTimedWriter[D, T]) Write(data *Data[D, T]) error {
	return w.buffer.Push(w.ctx, data)
}

func (w *BufferedTimedWriter[D, T]) loop() {
	defer w.Close()
	defer w.wg.Done()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			data, err := w.buffer.Pop(w.ctx)
			if err != nil {
				fmt.Printf("buffered timed writer error while popping from buffer; err: %v", err)
				return
			}

			if err := w.writer.Write(data); err != nil {
				fmt.Printf("buffered timed writer error while writing to the writer; err: %v", err)
				return
			}
		}
	}
}

func (w *BufferedTimedWriter[D, T]) Close() error {
	var err error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		if w.writer != nil {
			if err = w.writer.Close(); err != nil {
				return
			}
		}

		w.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}
