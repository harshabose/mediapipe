package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emirpasic/gods/v2/sets/hashset"

	"github.com/harshabose/tools/pkg/multierr"
)

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

	return p
}

func (p *FanoutPipe[D, T]) Start() {
	p.wg.Add(1)
	go p.loop()
}

func (p *FanoutPipe[D, T]) AddWriter(writer Writer[D, T]) {
	p.mux.Lock()
	defer p.mux.Unlock()

	p.writers.Add(writer)
}

func (p *FanoutPipe[D, T]) RemoveWriter(writer Writer[D, T]) {
	p.mux.Lock()
	defer p.mux.Unlock()

	p.writers.Remove(writer)
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
				fmt.Printf("fanout pipe error while reading: %v; stopping fanout pipe routine...\n", err)
				return
			}

			if err := p.write(data); err != nil {
				fmt.Printf("fanout pipe error while writing: %v; removing faulty writers...\n", err)
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
	p.mux.RLock()
	writers := p.writers.Values()
	p.mux.RUnlock()

	var err error

	for _, writer := range writers {
		if writerErr := writer.Write(data); writerErr != nil {
			err = multierr.Append(err, writerErr)
			p.RemoveWriter(writer)
		}
	}

	return err
}

func (p *FanoutPipe[D, T]) Close() error {
	var err error

	p.once.Do(func() {
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
