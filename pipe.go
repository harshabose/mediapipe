package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// CanGenerateConsume is a convenience composition of a source that can
// generate values of type T and a sink that can consume values of type D.
// It is useful for duplex endpoints that both produce and accept data.
type CanGenerateConsume[D, T any] interface {
	CanGenerate[T]
	CanConsume[D]
}

// ReaderWriter bundles a Reader and a Writer that transform in opposite
// directions: Reader produces `*Data[D, T]` and Writer accepts `*Data[T, D]`.
// This pairing is commonly used to build duplex pipes.
type ReaderWriter[D, T any] struct {
	Reader[D, T]
	Writer[T, D]
}

// Close closes the underlying Reader followed by the Writer, returning the
// first error encountered.
func (rw *ReaderWriter[D, T]) Close() error {
	if err := rw.Reader.Close(); err != nil {
		return err
	}

	if err := rw.Writer.Close(); err != nil {
		return err
	}

	return nil
}

// NewReaderWriter constructs a ReaderWriter from the provided reader and writer.
func NewReaderWriter[D, T any](reader Reader[D, T], writer Writer[T, D]) *ReaderWriter[D, T] {
	return &ReaderWriter[D, T]{
		Reader: reader,
		Writer: writer,
	}
}

// NewGeneralReaderWriter adapts a `CanGenerate[T]` and a `CanConsume[T]` into a
// ReaderWriter by creating a `GeneralReader[D, T]` with the provided
// transformer and a `GeneralWriter[T, D]`.
func NewGeneralReaderWriter[D, T any](generator CanGenerate[T], consumer CanConsume[T], transformer func(T) (D, error)) *ReaderWriter[D, T] {
	r := NewGeneralReader[D, T](generator, transformer)
	w := NewGeneralWriter[T, D](consumer, nil)

	return NewReaderWriter(r, w)
}

// NewIdentityGeneralReaderWriter pairs an identity Reader and Writer for the
// same type T.
func NewIdentityGeneralReaderWriter[T any](reader Reader[T, T], writer Writer[T, T]) *ReaderWriter[T, T] {
	return NewReaderWriter(reader, writer)
}

// Pipe is the minimal interface for a running connection between a Reader and
// a Writer. Start begins the data transfer, Close stops it, and Done is closed
// when the pipe terminates.
type Pipe[D, T any] interface {
	Start()
	Close()
	Done() <-chan struct{}
}

// SimplexPipe continuously reads from a Reader and writes to a Writer using an
// internal goroutine. It stops on context cancellation or upon read/write
// errors.
type SimplexPipe[D, T any] struct {
	reader Reader[D, T]
	writer Writer[D, T]

	once   sync.Once
	mux    sync.RWMutex
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSimplexPipe constructs a simplex pipe driven by the provided context.
func NewSimplexPipe[D, T any](ctx context.Context, reader Reader[D, T], writer Writer[D, T]) *SimplexPipe[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	p := &SimplexPipe[D, T]{
		reader: reader,
		writer: writer,
		ctx:    ctx2,
		cancel: cancel2,
	}

	return p
}

// Start launches the background loop that shuttles data from the Reader to the
// Writer.
func (p *SimplexPipe[D, T]) Start() {
	go p.loop(p.reader, p.writer)
}

func (p *SimplexPipe[D, T]) Reader() Reader[D, T] {
	return p.reader
}

func (p *SimplexPipe[D, T]) Writer() Writer[D, T] {
	return p.writer
}

// Done returns a channel that is closed when the pipe stops.
func (p *SimplexPipe[D, T]) Done() <-chan struct{} {
	return p.ctx.Done()
}

func (p *SimplexPipe[D, T]) loop(reader Reader[D, T], writer Writer[D, T]) {
	defer p.Close()

	p.wg.Add(1)
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			data, err := p.read(reader)
			if err != nil {
				fmt.Printf("pipe error while reading: %v\n", err)
				return
			}

			if err := p.write(writer, data); err != nil {
				fmt.Printf("pipe error while writing: %v\n", err)
				return
			}
		}
	}
}

func (p *SimplexPipe[D, T]) read(reader Reader[D, T]) (*Data[D, T], error) {
	if reader == nil {
		return nil, errors.New("reader is not set yet")
	}

	return reader.Read(p.ctx)
}

func (p *SimplexPipe[D, T]) write(writer Writer[D, T], data *Data[D, T]) error {
	if writer == nil {
		return errors.New("writer is not set yet")
	}

	return writer.Write(p.ctx, data)
}

// Close cancels the pipe's context and waits for the background goroutine to
// exit. It is safe to call multiple times.
func (p *SimplexPipe[D, T]) Close() {
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}

		p.wg.Wait()
	})
}

// DuplexPipe connects two ReaderWriter endpoints together in both directions
// by running two internal SimplexPipes.
type DuplexPipe[D, T any] struct {
	a *SimplexPipe[D, T]
	b *SimplexPipe[T, D]
}

// NewDuplexPipe constructs a DuplexPipe between two ReaderWriter endpoints.
func NewDuplexPipe[D, T any](ctx context.Context, rw1 *ReaderWriter[D, T], rw2 *ReaderWriter[T, D]) *DuplexPipe[D, T] {
	return &DuplexPipe[D, T]{
		a: NewSimplexPipe[D, T](ctx, rw1, rw2),
		b: NewSimplexPipe[T, D](ctx, rw2, rw1),
	}
}

// Start begins bidirectional transfer by starting both underlying simplex pipes.
func (p *DuplexPipe[D, T]) Start() {
	p.a.Start()
	p.b.Start()
}

func (p *DuplexPipe[D, T]) PipeA() *SimplexPipe[D, T] {
	return p.a
}

func (p *DuplexPipe[D, T]) PipeB() *SimplexPipe[T, D] {
	return p.b
}

// Done is closed when either direction of the duplex pipe stops.
func (p *DuplexPipe[D, T]) Done() <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		select {
		case <-p.a.Done():
			return
		case <-p.b.Done():
			return
		}
	}()

	return done
}

// Close stops both underlying simplex pipes.
func (p *DuplexPipe[D, T]) Close() {
	p.a.Close()
	p.b.Close()
}
