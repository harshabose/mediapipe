package mediapipe

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/buffer"
)

type Reader[D, T any] interface {
	Read() (*Data[D, T], error)
	io.Closer
}

type CanGenerate[T any] interface {
	Generate() (T, error)
}

type AnyReader[D, T any] struct {
	generator   CanGenerate[T]     // The source data generator
	transformer func(T) (D, error) // Function to transform T into D
}

func NewAnyReader[D, T any](generator CanGenerate[T], transformer func(T) (D, error)) *AnyReader[D, T] {
	return &AnyReader[D, T]{
		generator:   generator,
		transformer: transformer,
	}
}

func NewIdentityAnyReader[T any](generator CanGenerate[T]) *AnyReader[T, T] {
	return &AnyReader[T, T]{
		generator: generator,
		transformer: func(t T) (T, error) {
			return t, nil // Identity transformation
		},
	}
}

func (r *AnyReader[D, T]) Read() (*Data[D, T], error) {
	t, err := r.generator.Generate()
	if err != nil {
		return nil, err
	}

	if reflect.ValueOf(t).IsZero() {
		return nil, nil
	}

	e := Wrap[D, T](t, r.transformer)
	return e, nil
}

func (r *AnyReader[D, T]) Close() error {
	return nil
}

type BufferedReader[D, T any] struct {
	reader Reader[D, T]               // Direct reference to the reader
	buffer buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background reading loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

func NewBufferedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize int) *BufferedReader[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	r := &BufferedReader[D, T]{
		reader: reader,
		buffer: buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		ctx:    ctx2,
		cancel: cancel,
	}

	r.wg.Add(1)
	go r.loop()

	return r
}

func (r *BufferedReader[D, T]) Read() (*Data[D, T], error) {
	return r.buffer.Pop(r.ctx)
}

func (r *BufferedReader[D, T]) loop() {
	defer r.Close()
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			return
		default:
			data, err := r.reader.Read()
			if err != nil {
				fmt.Printf("buffered reader error while reading from the reader; err: %v", err)
				return
			}

			if err := r.buffer.Push(r.ctx, data); err != nil {
				fmt.Printf("buffered reader error while pushing into buffer; err: %v", err)
				return
			}
		}
	}
}

func (r *BufferedReader[D, T]) Close() error {
	var err error

	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		if r.reader != nil {
			if err = r.reader.Close(); err != nil {
				return
			}
		}

		r.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}

type BufferedTimedReader[D, T any] struct {
	reader   Reader[D, T]               // Direct reference to the reader
	buffer   buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements
	interval time.Duration              // Time interval between read operations

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background reading loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

func NewBufferedTimedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize int, interval time.Duration) *BufferedTimedReader[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	r := &BufferedTimedReader[D, T]{
		reader:   reader,
		buffer:   buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	r.wg.Add(1)
	go r.loop()

	return r
}

func (r *BufferedTimedReader[D, T]) Read() (*Data[D, T], error) {
	return r.buffer.Pop(r.ctx)
}

func (r *BufferedTimedReader[D, T]) loop() {
	defer r.Close()
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			data, err := r.reader.Read()
			if err != nil {
				fmt.Printf("buffered reader error while reading from the reader; err: %s", err.Error())
				return
			}

			if err := r.buffer.Push(r.ctx, data); err != nil {
				fmt.Printf("buffered reader error while pushing into buffer; err: %s", err.Error())
				return
			}
		}
	}
}

func (r *BufferedTimedReader[D, T]) Close() error {
	var err error

	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		if r.reader != nil {
			if err = r.reader.Close(); err != nil {
				return
			}
		}

		r.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}
