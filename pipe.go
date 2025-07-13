package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Pipe[D, T any] interface {
	Start()
	Pause()
	UnPause()
	Close() error
}

type CanAddReaderPipe[D, T any] interface {
	AddReader(Reader[D, T])
	RemoveReader(Reader[D, T])
}

type CanAddWriterPipe[D, T any] interface {
	AddWriter(Writer[D, T])
	RemoveWriter(Writer[D, T])
}

type AnyPipe[D, T any] struct {
	reader Reader[D, T]
	writer Writer[D, T]

	paused    bool
	pauseCond *sync.Cond

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
	mux    sync.RWMutex
}

func NewAnyPipe[D, T any](ctx context.Context, reader Reader[D, T], writer Writer[D, T]) *AnyPipe[D, T] {
	ctx2, cancel := context.WithCancel(ctx)

	p := &AnyPipe[D, T]{
		reader: reader,
		writer: writer,
		paused: false,
		ctx:    ctx2,
		cancel: cancel,
	}
	p.pauseCond = sync.NewCond(p.mux.RLocker())

	return p
}

func (p *AnyPipe[D, T]) Start() {
	p.wg.Add(1)
	go p.loop()
}

func (p *AnyPipe[D, T]) Pause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if !p.paused {
		p.paused = true
		fmt.Printf("AnyPipe paused\n")
	}
}

func (p *AnyPipe[D, T]) UnPause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.paused {
		p.paused = false
		p.pauseCond.Broadcast() // Wake up the processing loop
		fmt.Printf("AnyPipe resumed\n")
	}
}

func (p *AnyPipe[D, T]) loop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			if p.pauseAndCheckContext() {
				return
			}

			data, err := p.read()
			if err != nil {
				fmt.Printf("pipe error while reading: %v\n", err)
				return
			}

			if err := p.write(data); err != nil {
				fmt.Printf("pipe error while writing: %v\n", err)
				return
			}
		}
	}
}

func (p *AnyPipe[D, T]) pauseAndCheckContext() bool {
	p.mux.Lock()
	defer p.mux.Unlock()

	for p.paused {
		p.pauseCond.Wait()
	}

	select {
	case <-p.ctx.Done():
		return true
	default:
		return false
	}
}

func (p *AnyPipe[D, T]) read() (*Data[D, T], error) {
	p.mux.RLock()
	defer p.mux.RUnlock()

	if p.reader == nil {
		return nil, errors.New("reader is not ready yet")
	}

	return p.reader.Read()
}

func (p *AnyPipe[D, T]) write(data *Data[D, T]) error {
	p.mux.RLock()
	defer p.mux.RUnlock()

	if p.writer == nil {
		return errors.New("writer is not ready yet")
	}

	return p.writer.Write(data)
}

func (p *AnyPipe[D, T]) Close() error {
	var err error

	p.once.Do(func() {
		p.mux.Lock()
		defer p.mux.Unlock()

		if p.cancel != nil {
			p.cancel()
		}

		if p.reader != nil {
			if closeErr := p.reader.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}

		p.reader = nil

		if p.writer != nil {
			if closeErr := p.writer.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}

		p.writer = nil

		p.wg.Wait()
	})

	return err
}
