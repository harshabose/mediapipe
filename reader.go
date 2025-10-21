package mediapipe

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/buffer"
	"github.com/harshabose/tools/pkg/multierr"
	"github.com/harshabose/tools/pkg/set"
)

type Reader[D, T any] interface {
	Read() (*Data[D, T], error)
	io.Closer
}

type CanAddReader[D, T any] interface {
	AddReader(Reader[D, T])
}

type CanRemoveReader[D, T any] interface {
	RemoveReader(Reader[D, T])
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
	C      chan *Data[D, T]

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

	r.C = r.buffer.GetChannel()

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
				// NOTE: THIS WILL CRASH READER
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
	C        chan *Data[D, T]
	interval time.Duration // Time interval between read operations

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

	r.C = r.buffer.GetChannel()

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

type MultiReader[D, T any] struct {
	readers     *set.Set[Reader[D, T]]
	readerIndex int // Current reader index for round-robin scheduling
	mux         sync.RWMutex
}

func NewMultiReader[D, T any](readers ...Reader[D, T]) *MultiReader[D, T] {
	return &MultiReader[D, T]{
		readers:     set.NewSet(readers...),
		readerIndex: 0,
	}
}

func (r *MultiReader[D, T]) Read() (*Data[D, T], error) {
	r.mux.Lock()

	if r.readers.Size() == 0 {
		r.mux.Unlock()
		return nil, fmt.Errorf("no readers available")
	}

	if r.readerIndex >= r.readers.Size() {
		r.readerIndex = 0
	}

	reader := r.readers.Items()[r.readerIndex]
	r.readerIndex = (r.readerIndex + 1) % r.readers.Size()

	r.mux.Unlock()

	data, err := reader.Read()
	if err != nil {
		r.RemoveReader(reader)
		return nil, err
	}

	return data, nil
}

func (r *MultiReader[D, T]) GetReaders() []Reader[D, T] {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.readers.Items()
}

func (r *MultiReader[D, T]) Size() int {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.readers.Size()
}

func (r *MultiReader[D, T]) AddReader(reader Reader[D, T]) {
	r.mux.Lock()
	defer r.mux.Unlock()

	if reader == nil {
		return
	}

	r.readers.Add(reader)
}

func (r *MultiReader[D, T]) RemoveReader(reader Reader[D, T]) {
	r.mux.Lock()
	defer r.mux.Unlock()

	if reader == nil {
		return
	}

	r.readers.Remove(reader)
}

func (r *MultiReader[D, T]) Close() error {
	r.mux.Lock()
	defer r.mux.Unlock()

	var merr error

	for _, reader := range r.readers.Items() {
		if err := reader.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	return merr
}

type MultiReader2[D, T any] struct {
	reader      *set.Set[Reader[D, T]]
	bufsize     int
	readerAdded chan struct{} // Signal when reader is added

	once   sync.Once
	mux2   sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

func NewMultiReader2[D, T any](ctx context.Context, bufsize int, readers ...Reader[D, T]) *MultiReader2[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	r := &MultiReader2[D, T]{
		reader:      set.NewSet[Reader[D, T]](),
		bufsize:     bufsize,
		readerAdded: make(chan struct{}, 1),
		ctx:         ctx2,
		cancel:      cancel2,
	}

	if len(readers) > 0 {
		for _, reader := range readers {
			r.AddReader(reader)
		}
	}

	return r
}

func (r *MultiReader2[D, T]) AddReader(reader Reader[D, T]) {
	r.mux2.Lock()
	defer r.mux2.Unlock()

	select {
	case <-r.ctx.Done():
		return
	default:
		if reader == nil {
			return
		}

		bufr, ok := reader.(*BufferedReader[D, T])
		if !ok {
			bufr = NewBufferedReader(r.ctx, reader, r.bufsize)
		}

		r.reader.Add(bufr)

		// Signal that a reader was added (non-blocking)
		select {
		case r.readerAdded <- struct{}{}:
		default:
		}
	}
}

func (r *MultiReader2[D, T]) RemoveReader(reader Reader[D, T]) {
	r.mux2.Lock()
	defer r.mux2.Unlock()

	if reader == nil {
		return
	}

	bufr, ok := reader.(*BufferedReader[D, T])
	if !ok {
		return
	}

	r.reader.RemoveIf(func(r Reader[D, T]) bool {
		if r == nil {
			return false
		}

		bufr2, ok := r.(*BufferedReader[D, T])
		if !ok {
			return false
		}

		return bufr2 == bufr
	})
}

func (r *MultiReader2[D, T]) Read() (*Data[D, T], error) {
	for {
		r.mux2.RLock()
		size := r.reader.Size()
		r.mux2.RUnlock()

		if size == 0 {
			// Block until reader is added or context cancelled
			select {
			case <-r.readerAdded:
				continue // Retry reading
			case <-r.ctx.Done():
				return nil, r.ctx.Err()
			}
		}

		readers := r.reader.Items()
		cases := make([]reflect.SelectCase, 0, len(readers)+1)

		for _, reader := range readers {
			bufr, ok := reader.(*BufferedReader[D, T])
			if !ok {
				continue
			}
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(bufr.C),
			})
		}

		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(r.ctx.Done()),
		})

		chosen, value, ok := reflect.Select(cases)

		if chosen == len(cases)-1 {
			return nil, r.ctx.Err()
		}

		if !ok {
			// Channel closed, remove reader and retry
			r.mux2.Lock()
			if chosen < len(readers) {
				r.reader.Remove(readers[chosen])
			}
			r.mux2.Unlock()
			continue
		}

		data, ok := value.Interface().(*Data[D, T])
		if !ok {
			return nil, fmt.Errorf("unexpected data type from reader")
		}

		return data, nil
	}
}

func (r *MultiReader2[D, T]) Close() error {
	var merr error = nil
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		for _, reader := range r.reader.Items() {
			if err := reader.Close(); err != nil {
				merr = multierr.Append(merr, err)
			}
		}
	})

	return merr
}

type SwappableReader[D, T any] struct {
	reader Reader[D, T]
	mux    sync.RWMutex
}

func NewSwappableReader[D, T any](reader Reader[D, T]) *SwappableReader[D, T] {
	return &SwappableReader[D, T]{
		reader: reader,
	}
}

func (r *SwappableReader[D, T]) Swap(reader Reader[D, T]) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if err := r.reader.Close(); err != nil {
		return fmt.Errorf("error while swapping reader (err: %v)", err)
	}

	r.reader = reader
	return nil
}

func (r *SwappableReader[D, T]) Read() (*Data[D, T], error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.reader.Read()
}

func (r *SwappableReader[D, T]) Close() error {
	r.mux.Lock()
	defer r.mux.Unlock()

	return r.reader.Close()
}
