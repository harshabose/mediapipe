package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emirpasic/gods/v2/sets/linkedhashset"

	"github.com/harshabose/tools/pkg/multierr"
)

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

	return p
}

func (p *MergePipe[D, T]) Start() {
	p.wg.Add(1)
	go p.loop()
}

func (p *MergePipe[D, T]) AddReader(reader Reader[D, T]) {
	p.mux.Lock()
	defer p.mux.Unlock()

	p.readers.Add(reader)
}

func (p *MergePipe[D, T]) RemoveReader(reader Reader[D, T]) {
	p.mux.Lock()
	defer p.mux.Unlock()

	p.readers.Remove(reader)
}

func (p *MergePipe[D, T]) Pause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if !p.paused {
		p.paused = true
	}
}

func (p *MergePipe[D, T]) UnPause() {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.paused {
		p.paused = false
		p.pauseCond.Broadcast() // Wake up the processing loop
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
				continue
			}

			if err := p.write(data); err != nil {
				fmt.Printf("pipe error while writing: %v. Removing faulty readers...\n", err)
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

	if p.readers.Size() == 0 {
		return nil, errors.New("no readers available")
	}

	readers := p.readers.Values()
	if p.readerIndex >= len(readers) {
		p.readerIndex = 0
	}

	reader := readers[p.readerIndex]
	p.readerIndex = (p.readerIndex + 1) % len(readers)

	p.mux.Unlock()

	data, err := reader.Read()
	if err != nil {
		p.RemoveReader(reader)
	}

	return data, nil
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
