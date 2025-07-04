package mediasink

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emirpasic/gods/v2/sets/hashset"

	"github.com/harshabose/mediasink/internal/utils/multierr"
)

type writerWrap[D, T any] struct {
	writer Writer[D, T]
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	mux    sync.RWMutex
}

func newWriterWrap[D, T any](ctx context.Context, w Writer[D, T]) (*writerWrap[D, T], context.Context, context.CancelFunc) {
	ctx2, cancel := context.WithCancel(ctx)
	return &writerWrap[D, T]{
		writer: w,
		ctx:    ctx2,
		cancel: cancel,
	}, ctx2, cancel
}

func (w *writerWrap[D, T]) Write(data *Data[D, T]) error {
	select {
	case <-w.ctx.Done():
		return errors.New("context cancelled")
	default:
		if err := w.write(data); err != nil {
			if cErr := w.Close(); err != nil {
				return multierr.Append(err, cErr)
			}
			return err
		}
	}

	return nil
}

func (w *writerWrap[D, T]) write(data *Data[D, T]) error {
	w.mux.RLock()
	defer w.mux.RUnlock()

	if w.writer == nil {
		return errors.New("writer not in ready state")
	}

	return w.writer.Write(data)
}

func (w *writerWrap[D, T]) Close() error {
	var err error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		w.mux.Lock()
		defer w.mux.Unlock()

		err = w.writer.Close()
	})

	if err != nil {
		return fmt.Errorf("error while closing writerWrap: %w", err)
	}

	return nil
}

type FanoutPipe[D, T any] struct {
	reader  Reader[D, T]
	writers *hashset.Set[Writer[D, T]] // Multiple destinations for Data elements

	// Flow control
	paused    bool       // Whether the pipe is currently paused
	pauseCond *sync.Cond // Condition variable for pause/resume coordination

	ctx    context.Context    // Context for cancellation and lifecycle management
	cancel context.CancelFunc // Function to cancel the background processing loop
	once   sync.Once          // Ensures Close() can be called multiple times safely
	wg     sync.WaitGroup     // Synchronises background goroutine shutdown
	mux    sync.RWMutex       // Protects concurrent access to readers/writer
}

func NewFanoutPipe[D, T any](ctx context.Context, reader Reader[D, T], writers ...Writer[D, T]) *FanoutPipe[D, T] {
	ctx2, cancel := context.WithCancel(ctx)

	p := &FanoutPipe[D, T]{
		reader:  reader,
		writers: hashset.New[Writer[D, T]](writers...),
		paused:  false,
		ctx:     ctx2,
		cancel:  cancel,
	}
	p.pauseCond = sync.NewCond(&p.mux)

	p.wg.Add(1)
	go p.loop()

	return p
}

func (p *FanoutPipe[D, T]) Wait() <-chan struct{} {
	return p.ctx.Done()
}

func (p *FanoutPipe[D, T]) AddWriter(w Writer[D, T]) (context.Context, context.CancelFunc) {
	writer, ctx, cancel := newWriterWrap(p.ctx, w)
	context.AfterFunc(ctx, func() {
		p.mux.Lock()
		defer p.mux.Unlock()

		if p.writers != nil {
			p.writers.Remove(writer)
		}
	})

	p.mux.Lock()
	defer p.mux.Unlock()

	p.writers.Add(writer)

	return ctx, cancel
}

func (p *FanoutPipe[D, T]) Pause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if !p.paused {
		p.paused = true
		fmt.Printf("AnyPipe paused\n")
	}
}

func (p *FanoutPipe[D, T]) UnPause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.paused {
		p.paused = false
		p.pauseCond.Broadcast() // Wake up the processing loop
		fmt.Printf("AnyPipe resumed\n")
	}
}

func (p *FanoutPipe[D, T]) loop() {
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
				fmt.Printf("pipe error while reading: %v; stopping fanout pipe routine...", err)
				return
			}

			if err := p.write(data); err != nil {
				fmt.Printf("pipe error while writing: %v", err)
				continue
			}
		}
	}
}

func (p *FanoutPipe[D, T]) pauseAndCheckContext() bool {
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

func (p *FanoutPipe[D, T]) read() (*Data[D, T], error) {
	p.mux.RLock()
	defer p.mux.RUnlock()

	if p.reader == nil {
		return nil, errors.New("reader is nil")
	}

	return p.reader.Read()
}

func (p *FanoutPipe[D, T]) write(data *Data[D, T]) error {
	p.mux.RLock() // DEAD LOCK
	defer p.mux.RUnlock()

	writers := p.writers.Values()

	var err error

	for _, writer := range writers {
		if writerErr := writer.Write(data); writerErr != nil {
			err = multierr.Append(err, writerErr)
		}
	}

	return err
}

func (p *FanoutPipe[D, T]) Close() error {
	var err error

	p.once.Do(func() { // DEAD LOCK
		if p.cancel != nil {
			p.cancel()
		}

		p.wg.Wait()

		p.mux.Lock()
		defer p.mux.Unlock()

		if p.reader != nil {
			if readerErr := p.reader.Close(); readerErr != nil {
				err = multierr.Append(err, readerErr)
			}
		}

		writers := p.writers.Values()
		for _, writer := range writers {
			if writerErr := writer.Close(); writerErr != nil {
				err = multierr.Append(err, writerErr)
			}
		}

		p.writers = nil
		p.reader = nil
	})

	if err != nil {
		return fmt.Errorf("error while closing fanout pipe: %w", err)
	}
	return nil
}
