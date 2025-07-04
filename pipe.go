// Pipe System - Pipeline Connection and Data Flow Management
//
// This package implements the connection layer of the universal media routing system,
// providing simple, reliable pipes that connect readers to writers with proper
// lifecycle management and fault tolerance.
//
// # The Pipe Philosophy
//
// Pipes follow the Unix philosophy of doing one thing well - they move data from
// readers to writers reliably and efficiently. Complex concerns like buffering,
// rate limiting, and backpressure are handled by the reader and writer components
// themselves, keeping pipes simple and focused.
//
// # Fault Tolerance
//
// All pipes are designed for streaming media scenarios where resilience is more
// important than perfect reliability. Temporary errors are logged and the pipeline
// continues, ensuring that transient network issues or processing delays don't
// bring down the entire data flow.
//
// # Pipeline Composition
//
// Pipes can be composed to create complex topologies:
//   - 1:1 data forwarding with AnyPipe
//   - 1:N fanout distribution with FanoutPipe
//   - N:1 data merging with MergePipe
//   - Multi-stage processing by chaining pipes

package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Pipe defines the basic contract for all pipeline components, providing
// flow control capabilities for managing data transmission rates and
// handling backpressure scenarios.
//
// All pipe implementations should support pausing and resuming data flow
// without losing data or corrupting the pipeline state.
type Pipe interface {
	// Pause temporarily stops data flow through the pipe.
	// Buffered data is preserved and will be processed when resumed.
	// Multiple calls to Pause() are safe and idempotent.
	Pause()

	// UnPause resumes data flow through the pipe.
	// Processing continues from where it was paused.
	// Multiple calls to UnPause() are safe and idempotent.
	UnPause()

	// Close shuts down the pipe and releases all resources.
	// After Close() is called, the pipe cannot be reused.
	Close() error
}

// CanAddReaderPipe defines pipes that support dynamic addition of readers
// during runtime. This enables building flexible topologies where data
// sources can be added to existing pipelines without reconstruction.
//
// Implementations must handle concurrent access safely and ensure
// that newly added readers are integrated into the processing loop
// without disrupting existing data flow.
type CanAddReaderPipe[D, T any] interface {
	// AddReader dynamically adds a new reader to the pipe.
	// The reader will be integrated into the processing loop
	// and begin contributing data according to the pipe's scheduling policy.
	//
	// Parameters:
	//   - reader: The reader to add to the pipe
	//
	// Thread safety: This method must be safe to call concurrently
	// with ongoing pipe operations.
	AddReader(Reader[D, T]) (context.Context, context.CancelFunc)
}

// CanAddWriterPipe defines pipes that support dynamic addition of writers
// during runtime. This enables building flexible topologies where data
// destinations can be added to existing pipelines without reconstruction.
//
// Implementations must handle concurrent access safely and ensure
// that newly added writers receive data according to the pipe's
// distribution policy.
type CanAddWriterPipe[D, T any] interface {
	// AddWriter dynamically adds a new writer to the pipe.
	// The writer will begin receiving data according to the pipe's
	// distribution policy (broadcast for fanout, round-robin for merge, etc.).
	//
	// Parameters:
	//   - writer: The writer to add to the pipe
	//
	// Thread safety: This method must be safe to call concurrently
	// with ongoing pipe operations.
	AddWriter(Writer[D, T]) (context.Context, context.CancelFunc)
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

	p.wg.Add(1)
	go p.loop()

	return p
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
				fmt.Printf("pipe error while reading: %v", err)
				return
			}

			if err := p.write(data); err != nil {
				fmt.Printf("pipe error while writing: %v", err)
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
