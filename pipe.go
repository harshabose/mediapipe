package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type CanGenerateConsume[D, T any] interface {
	CanGenerate[T]
	CanConsume[D]
}

type ReaderWriter[D, T any] interface {
	Reader[D, T]
	Writer[T, D]
}

type AnyReaderWriter[D, T any] struct {
	Reader[D, T]
	Writer[T, D]
}

func (rw *AnyReaderWriter[D, T]) Close() error {
	if err := rw.Reader.Close(); err != nil {
		return err
	}

	if err := rw.Writer.Close(); err != nil {
		return err
	}

	return nil
}

func NewReaderWriter[D, T any](reader Reader[D, T], writer Writer[T, D]) *AnyReaderWriter[D, T] {
	return &AnyReaderWriter[D, T]{
		Reader: reader,
		Writer: writer,
	}
}

func NewAnyReaderWriter[D, T any](generator CanGenerate[T], consumer CanConsume[T], transformer func(T) (D, error)) *AnyReaderWriter[D, T] {
	r := NewAnyReader[D, T](generator, transformer)
	w := NewAnyWriter[T, D](consumer, nil)

	return NewReaderWriter(r, w)
}

type Pipe[D, T any] interface {
	Start()
	Close()
	Done() <-chan struct{}
}

type AnyPipe[D, T any] struct {
	reader Reader[D, T]
	writer Writer[D, T]

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
}

func NewAnyPipe[D, T any](ctx context.Context, reader Reader[D, T], writer Writer[D, T]) *AnyPipe[D, T] {
	ctx2, cancel := context.WithCancel(ctx)

	p := &AnyPipe[D, T]{
		reader: reader,
		writer: writer,
		ctx:    ctx2,
		cancel: cancel,
	}

	return p
}

func (p *AnyPipe[D, T]) Start() {
	p.wg.Add(1)

	go p.loop(p.reader, p.writer)
}

func (p *AnyPipe[D, T]) Done() <-chan struct{} {
	return p.ctx.Done()
}

func (p *AnyPipe[D, T]) loop(reader Reader[D, T], writer Writer[D, T]) {
	defer p.Close()
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

func (p *AnyPipe[D, T]) read(reader Reader[D, T]) (*Data[D, T], error) {
	if reader == nil {
		return nil, errors.New("reader is not set yet")
	}

	return reader.Read()
}

func (p *AnyPipe[D, T]) write(writer Writer[D, T], data *Data[D, T]) error {
	if writer == nil {
		return errors.New("writer is not set yet")
	}

	return writer.Write(data)
}

func (p *AnyPipe[D, T]) Close() {
	// TODO: BE CAREFUL OF SYNC.ONCE
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}

		p.wg.Wait()

		p.reader = nil
		p.writer = nil
	})
}

type AnyDuplexPipe[D, T any] struct {
	a *AnyPipe[D, T]
	b *AnyPipe[T, D]
}

func NewAnyDuplexPipe[D, T any](ctx context.Context, rw1 ReaderWriter[D, T], rw2 ReaderWriter[T, D]) *AnyDuplexPipe[D, T] {
	return &AnyDuplexPipe[D, T]{
		a: NewAnyPipe(ctx, rw1, rw2),
		b: NewAnyPipe(ctx, rw2, rw1),
	}
}

func (p *AnyDuplexPipe[D, T]) Start() {
	p.a.Start()
	p.b.Start()
}

func (p *AnyDuplexPipe[D, T]) Done() <-chan struct{} {
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

func (p *AnyDuplexPipe[D, T]) Close() {
	p.a.Close()
	p.b.Close()
}
