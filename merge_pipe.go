package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emirpasic/gods/v2/sets/linkedhashset"

	"github.com/harshabose/mediasink/internal/utils/multierr"
)

type readerWrap[D, T any] struct {
	reader Reader[D, T]
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	mux    sync.RWMutex
}

func newReaderWrap[D, T any](ctx context.Context, r Reader[D, T]) (*readerWrap[D, T], context.Context, context.CancelFunc) {
	ctx2, cancel := context.WithCancel(ctx)
	return &readerWrap[D, T]{
		reader: r,
		ctx:    ctx2,
		cancel: cancel,
	}, ctx2, cancel
}

func (r *readerWrap[D, T]) Read() (*Data[D, T], error) {
	select {
	case <-r.ctx.Done():
		return nil, errors.New("context cancelled")
	default:
		data, err := r.read()
		if err != nil {
			if cErr := r.Close(); err != nil {
				return nil, multierr.Append(err, cErr)
			}
		}

		return data, nil
	}
}

func (r *readerWrap[D, T]) read() (*Data[D, T], error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	if r.reader == nil {
		return nil, errors.New("reader is nil")
	}

	return r.reader.Read()
}

func (r *readerWrap[D, T]) Close() error {
	var err error

	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		r.mux.Lock()
		defer r.mux.Unlock()

		err = r.reader.Close()
	})

	if err != nil {
		return fmt.Errorf("error while closing writerWrap: %r", err)
	}

	return nil
}

type MergePipe[D, T any] struct {
	writer  Writer[D, T]
	readers *linkedhashset.Set[Reader[D, T]]

	paused    bool
	pauseCond *sync.Cond

	// Round-robin state with bounds checking
	readerIndex int // Current reader index for round-robin scheduling

	ctx    context.Context    // Context for cancellation and lifecycle management
	cancel context.CancelFunc // Function to cancel the background processing loop
	once   sync.Once          // Ensures Close() can be called multiple times safely
	wg     sync.WaitGroup     // Synchronises background goroutine shutdown
	mux    sync.RWMutex       // Protects concurrent access to readers/writer
}

func NewMergePipe[D, T any](ctx context.Context, writer Writer[D, T], readers ...Reader[D, T]) *MergePipe[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	p := &MergePipe[D, T]{
		readers: linkedhashset.New(readers...),
		writer:  writer,
		paused:  false,
		ctx:     ctx2,
		cancel:  cancel,
	}
	p.pauseCond = sync.NewCond(&p.mux)

	p.wg.Add(1)
	go p.loop()

	return p
}

func (p *MergePipe[D, T]) AddReader(r Reader[D, T]) (context.Context, context.CancelFunc) {
	reader, ctx, cancel := newReaderWrap(p.ctx, r)
	context.AfterFunc(ctx, func() {
		p.mux.Lock()
		defer p.mux.Unlock()

		p.readers.Remove(reader)
	})

	p.mux.Lock()
	defer p.mux.Unlock()

	p.readers.Add(reader)

	return ctx, cancel
}

func (p *MergePipe[D, T]) Wait() <-chan struct{} {
	return p.ctx.Done()
}

func (p *MergePipe[D, T]) Pause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if !p.paused {
		p.paused = true
		fmt.Printf("AnyPipe paused\n")
	}
}

func (p *MergePipe[D, T]) UnPause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.paused {
		p.paused = false
		p.pauseCond.Broadcast() // Wake up the processing loop
		fmt.Printf("AnyPipe resumed\n")
	}
}

func (p *MergePipe[D, T]) loop() {
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
				fmt.Printf("pipe error while reading: %v", err)
				continue
			}

			if err := p.write(data); err != nil {
				fmt.Printf("pipe error while writing: %v", err)
				return
			}
		}
	}
}

func (p *MergePipe[D, T]) pauseAndCheckContext() bool {
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

func (p *MergePipe[D, T]) read() (*Data[D, T], error) {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.readers.Size() == 0 {
		return nil, errors.New("no readers available")
	}

	readers := p.readers.Values()

	if p.readerIndex >= len(readers) {
		p.readerIndex = 0
	}

	reader := readers[p.readerIndex]

	p.readerIndex = (p.readerIndex + 1) % len(readers)

	return reader.Read()
}

func (p *MergePipe[D, T]) write(data *Data[D, T]) error {
	p.mux.RLock()
	defer p.mux.RUnlock()

	if data == nil {
		return nil
	}

	if p.writer == nil {
		return errors.New("writer not ready yet")
	}

	return p.writer.Write(data)
}

func (p *MergePipe[D, T]) Close() error {
	var err error

	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}

		p.wg.Wait()

		p.mux.Lock()
		defer p.mux.Unlock()

		if p.writer != nil {
			if writerErr := p.writer.Close(); writerErr != nil {
				err = multierr.Append(err, writerErr)
			}
		}

		readers := p.readers.Values()
		for _, reader := range readers {
			if readerErr := reader.Close(); readerErr != nil {
				err = multierr.Append(err, readerErr)
			}
		}

		p.writer = nil
		p.readers = nil
	})

	if err != nil {
		return fmt.Errorf("error while closing fanout pipe: %w", err)
	}
	return nil
}
