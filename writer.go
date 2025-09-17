package mediapipe

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/buffer"
	"github.com/harshabose/tools/pkg/multierr"
	"github.com/harshabose/tools/pkg/set"
)

type Writer[D, T any] interface {
	Write(*Data[D, T]) error
	io.Closer
}

type CanAddWriter[D, T any] interface {
	AddWriter(Writer[D, T])
}

type CanRemoveWriter[D, T any] interface {
	RemoveWriter(Writer[D, T])
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

func NewAnyMultiWriters[D, T any](transformer func(D) (T, error), consumers []CanConsume[D]) []*AnyWriter[D, T] {
	var writers = make([]*AnyWriter[D, T], 0)

	for _, consumer := range consumers {
		writers = append(writers, NewAnyWriter[D, T](consumer, transformer))
	}

	return writers
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
	C      chan *Data[D, T]

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

	w.C = w.buffer.GetChannel()

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
	C        chan *Data[D, T]

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

	w.C = w.buffer.GetChannel()

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

type MultiWriter[D, T any] struct {
	writers *set.Set[Writer[D, T]]
	mux     sync.RWMutex
}

func NewMultiWriter[D, T any](writers ...Writer[D, T]) *MultiWriter[D, T] {
	return &MultiWriter[D, T]{
		writers: set.NewSet(writers...),
	}
}

func (w *MultiWriter[D, T]) Write(data *Data[D, T]) error {
	w.mux.RLock()
	writers := w.writers.Items()
	w.mux.RUnlock()

	if len(writers) == 0 {
		return nil
	}

	var merr error

	for _, writer := range writers {
		if err := writer.Write(data); err != nil {
			merr = multierr.Append(merr, err)
			w.RemoveWriter(writer)
		}
	}

	return merr
}

func (w *MultiWriter[D, T]) AddWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	w.writers.Add(writer)
}

func (w *MultiWriter[D, T]) RemoveWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	w.writers.Remove(writer)
}

func (w *MultiWriter[D, T]) Close() error {
	w.mux.Lock()
	defer w.mux.Unlock()

	var merr error

	for _, writer := range w.writers.Items() {
		if err := writer.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	return merr
}

type MultiWriter2[D, T any] struct {
	*MultiWriter[D, T]
	bufsize int

	ctx    context.Context
	cancel context.CancelFunc
}

func NewMultiWriter2[D, T any](ctx context.Context, bufsize int, writers ...Writer[D, T]) *MultiWriter2[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	return &MultiWriter2[D, T]{
		MultiWriter: NewMultiWriter(writers...),
		bufsize:     bufsize,
		ctx:         ctx2,
		cancel:      cancel2,
	}
}

func (w *MultiWriter2[D, T]) AddWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	bufw, ok := writer.(*BufferedWriter[D, T])
	if !ok {
		bufw = NewBufferedWriter(w.ctx, writer, w.bufsize)
	}

	w.writers.Add(bufw)
}

func (w *MultiWriter2[D, T]) RemoveWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	w.writers.RemoveIf(func(w Writer[D, T]) bool {
		bufw, ok := w.(*BufferedWriter[D, T])
		if !ok {
			return false
		}

		return bufw.writer == writer
	})
}

type SwappableWriter[D, T any] struct {
	writer Writer[D, T]
	mux    sync.RWMutex
}

func NewSwappableWriter[D, T any](writer Writer[D, T]) *SwappableWriter[D, T] {
	return &SwappableWriter[D, T]{
		writer: writer,
	}
}

func (w *SwappableWriter[D, T]) Swap(writer Writer[D, T]) error {
	w.mux.Lock()
	defer w.mux.Unlock()

	if err := w.writer.Close(); err != nil {
		return fmt.Errorf("error while swapping writer (err: %v)", err)
	}

	w.writer = writer
	return nil
}

func (w *SwappableWriter[D, T]) Write(data *Data[D, T]) error {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.writer.Write(data)
}

func (w *SwappableWriter[D, T]) Close() error {
	w.mux.Lock()
	defer w.mux.Unlock()

	return w.writer.Close()
}
