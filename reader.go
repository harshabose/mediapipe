package mediapipe

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/harshabose/tools/pkg/buffer"
	"github.com/harshabose/tools/pkg/cond"
	"github.com/harshabose/tools/pkg/multierr"
	"github.com/harshabose/tools/pkg/set"
)

// Reader pulls source values of type T from a generator and exposes them as
// `*Data[D, T]` units, where the embedded transformer can derive D from T on
// demand. Implementations should honor the provided context for cancellation
// and deadlines, and return io.EOF to signal a clean end of stream.
type Reader[D, T any] interface {
	Read(context.Context) (*Data[D, T], error)
	io.Closer
}

// CanAddReader is implemented by types that support dynamically adding a
// Reader to an existing composite (for example, a multi-reader).
type CanAddReader[D, T any] interface {
	AddReader(Reader[D, T])
}

// CanRemoveReader is implemented by types that support removing a Reader from
// an existing composite.
type CanRemoveReader[D, T any] interface {
	RemoveReader(Reader[D, T])
}

// CanGenerate abstracts a source that can produce values of type T on demand.
// Generate should block until a value is available, the context is canceled,
// or an unrecoverable error occurs.
type CanGenerate[T any] interface {
	Generate(context.Context) (T, error)
}

type CanGenerateFunc[T any] func(context.Context) (T, error)

func (f CanGenerateFunc[T]) Generate(ctx context.Context) (T, error) {
	return f(ctx)
}

// GeneralReader adapts a `CanGenerate[T]` into a `Reader[D, T]` by pairing it
// with a transformation function that converts T into D when `Data.Get` is
// called. This keeps generation and transformation responsibilities separate.
type GeneralReader[D, T any] struct {
	generator   CanGenerate[T]     // The source data generator
	transformer func(T) (D, error) // Function to transform T into D
}

// NewGeneralReader constructs a `GeneralReader` from a value generator and a
// transformer function used by produced `Data` instances.
func NewGeneralReader[D, T any](generator CanGenerate[T], transformer func(T) (D, error)) *GeneralReader[D, T] {
	return &GeneralReader[D, T]{
		generator:   generator,
		transformer: transformer,
	}
}

// NewIdentityGeneralReader constructs a `GeneralReader` whose transformer is
// the identity function; i.e., T is passed through as D unchanged.
func NewIdentityGeneralReader[T any](generator CanGenerate[T]) *GeneralReader[T, T] {
	return &GeneralReader[T, T]{
		generator: generator,
		transformer: func(t T) (T, error) {
			return t, nil // Identity transformation
		},
	}
}

// Read pulls a value from the underlying generator and wraps it into a `Data`
// using the configured transformer. If the generated value is a zero value that
// this package treats as empty, `nil, nil` is returned to allow callers to skip
// processing without treating it as an error.
func (r *GeneralReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	t, err := r.generator.Generate(ctx)
	if err != nil {
		return nil, err
	}

	if isZeroSafe(t) {
		return nil, nil
	}

	e := Wrap[D, T](t, r.transformer)
	return e, nil
}

// Close implements io.Closer. GeneralReader has no resources of its own, so it
// returns nil.
func (r *GeneralReader[D, T]) Close() error {
	return nil
}

// TimeoutReader decorates another Reader, applying a per-call timeout to Read
// operations via context.WithTimeout.
type TimeoutReader[D, T any] struct {
	r       Reader[D, T]
	timeout time.Duration
}

// NewTimeoutReader wraps an existing Reader and enforces a timeout for each
// call to Read.
func NewTimeoutReader[D, T any](reader Reader[D, T], timeout time.Duration) *TimeoutReader[D, T] {
	return &TimeoutReader[D, T]{
		r:       reader,
		timeout: timeout,
	}
}

// Read invokes the wrapped Reader with a derived context that times out after
// the configured duration.
func (r *TimeoutReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	ctx2, cancel2 := context.WithTimeout(ctx, r.timeout)
	defer cancel2()

	return r.r.Read(ctx2)
}

// Close propagates Close to the wrapped Reader.
func (r *TimeoutReader[D, T]) Close() error {
	return r.r.Close()
}

// BufferedReader continuously pulls from an underlying Reader and stores
// produced `Data` items into an internal buffer, decoupling production and
// consumption rates. Reads pop from the buffer rather than the source.
type BufferedReader[D, T any] struct {
	reader Reader[D, T]
	buffer buffer.Buffer[*Data[D, T]]

	wg     sync.WaitGroup
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBufferedReader creates a BufferedReader that uses a channel-backed buffer
// of the given size.
func NewBufferedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize uint) *BufferedReader[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	r := &BufferedReader[D, T]{
		reader: reader,
		buffer: buffer.NewChannelBuffer[*Data[D, T]](ctx2, bufsize, 1),
		ctx:    ctx2,
		cancel: cancel2,
	}

	go r.loop()

	return r
}

// NewBufferedReaderWithBuffer is a constructor of BufferedReader that accepts a custom buffer
// implementation.
func NewBufferedReaderWithBuffer[D, T any](ctx context.Context, reader Reader[D, T], buffer buffer.Buffer[*Data[D, T]]) *BufferedReader[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	r := &BufferedReader[D, T]{
		reader: reader,
		buffer: buffer,
		ctx:    ctx2,
		cancel: cancel2,
	}

	go r.loop()

	return r
}

func (r *BufferedReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	return r.buffer.Pop(ctx)
}

func (r *BufferedReader[D, T]) loop() {
	defer r.close()

	r.wg.Add(1)
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			return
		default:
			data, err := r.reader.Read(r.ctx)
			if err != nil {
				// fmt.Printf("buffered reader error while reading from the reader; err: %v", err)
				return
			}

			if err := r.buffer.Push(r.ctx, data); err != nil {
				// fmt.Printf("buffered reader error while pushing into buffer; err: %v", err)
				return
			}
		}
	}
}

func (r *BufferedReader[D, T]) close() {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		// r.wg.Wait() // TODO: this is causing to wait for the reader who does not honour ctx; fix this asap
	})
}

// Close stops the background loop, closes the buffer, and then closes the
// wrapped Reader.
func (r *BufferedReader[D, T]) Close() error {
	r.close()

	r.buffer.Close()

	if r.reader != nil {
		if err := r.reader.Close(); err != nil {
			return err
		}
	}

	return nil
}

// BufferedTimedReader is similar to BufferedReader, but pulls from the
// underlying Reader on a fixed time interval using a ticker. This is useful
// when the source should be sampled periodically regardless of consumer speed.
type BufferedTimedReader[D, T any] struct {
	reader   Reader[D, T]
	buffer   buffer.Buffer[*Data[D, T]]
	interval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewBufferedTimedReader creates a `BufferedTimedReader` with a channel-backed
// buffer of the given size and a fixed read interval.
func NewBufferedTimedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize int, interval time.Duration) *BufferedTimedReader[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	r := &BufferedTimedReader[D, T]{
		reader:   reader,
		buffer:   buffer.NewChannelBuffer[*Data[D, T]](ctx2, uint(bufsize), 1),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	go r.loop()

	return r
}

// Read pops the next `Data` item from the internal buffer, blocking until
// available or the context is canceled.
func (r *BufferedTimedReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	return r.buffer.Pop(ctx)
}

func (r *BufferedTimedReader[D, T]) loop() {
	defer r.close()

	r.wg.Add(1)
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			data, err := r.reader.Read(r.ctx)
			if err != nil {
				// fmt.Printf("buffered reader error while reading from the reader; err: %s", err.Error())
				return
			}

			if err := r.buffer.Push(r.ctx, data); err != nil {
				// fmt.Printf("buffered reader error while pushing into buffer; err: %s", err.Error())
				return
			}
		}
	}
}

func (r *BufferedTimedReader[D, T]) close() {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		r.wg.Wait()
	})
}

// Close stops the background loop, closes the buffer, and closes the wrapped
// reader.
func (r *BufferedTimedReader[D, T]) Close() error {
	r.close()

	r.buffer.Close()

	if r.reader != nil {
		if err := r.reader.Close(); err != nil {
			return err
		}
	}

	return nil
}

// MultiReader multiplexes over a dynamic set of Readers and reads from them in
// round-robin order. Readers that error are removed automatically.
//
// Concurrency note: Read services one underlying Reader per call, but can be
// called in a tight loop or from multiple goroutines to simulate concurrent
// reads. The observed concurrency depends on call frequency and scheduling.
type MultiReader[D, T any] struct {
	readers *set.Set[Reader[D, T]]

	index int // Current reader index for round-robin scheduling
	cond  *cond.ContextCond
	mux   sync.RWMutex
}

// NewMultiReader constructs a MultiReader with an optional initial set of
// Readers.
func NewMultiReader[D, T any](readers ...Reader[D, T]) *MultiReader[D, T] {
	r := &MultiReader[D, T]{
		readers: set.NewSet(readers...),
		index:   0,
	}
	r.cond = cond.NewContextCond(&r.mux)

	return r
}

// Read obtains the next Reader according to round-robin policy and delegates
// the read to it.
func (r *MultiReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	reader, err := r.getNextReader(ctx)
	if err != nil {
		return nil, err
	}

	data, err := reader.Read(ctx)
	if err != nil {
		r.RemoveReader(reader)
		return nil, err
	}

	return data, nil
}

// getNextReader is a blocking helper that waits for at least one Reader to be
// present, then returns the next Reader by index.
func (r *MultiReader[D, T]) getNextReader(ctx context.Context) (Reader[D, T], error) {
	r.mux.Lock()
	defer r.mux.Unlock()

	for r.readers.Size() == 0 {
		if err := r.cond.Wait(ctx); err != nil {
			return nil, err
		}
	}

	if r.index >= r.readers.Size() {
		r.index = 0
	}

	reader := r.readers.Items()[r.index]
	r.index = (r.index + 1) % r.readers.Size()

	return reader, nil
}

// Size returns the number of Readers managed by the MultiReader.
func (r *MultiReader[D, T]) Size() int {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.readers.Size()
}

// AddReader adds a Reader to the round-robin set and signals waiting readers.
func (r *MultiReader[D, T]) AddReader(reader Reader[D, T]) {
	r.mux.Lock()
	defer r.mux.Unlock()

	if reader == nil {
		return
	}

	r.readers.Add(reader)

	r.cond.Broadcast()
}

// RemoveReader removes a Reader from the set and closes it if present.
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

// MultiReader2 fan-in merges multiple Readers into a single buffer by wrapping
// each added Reader with a BufferedReader that pushes into a shared buffer.
// Removing a Reader stops its associated buffering loop but does not close the
// shared buffer until `Close` is called.
type MultiReader2[D, T any] struct {
	readers *set.SafeSet[*BufferedReader[D, T]]
	buffer  buffer.Buffer[*Data[D, T]]

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewMultiReader2 constructs a MultiReader2 with a shared channel buffer of the
// given size and optional initial Readers.
func NewMultiReader2[D, T any](ctx context.Context, bufsize uint, readers ...Reader[D, T]) *MultiReader2[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	r := &MultiReader2[D, T]{
		readers: set.NewSafeSet[*BufferedReader[D, T]](),
		buffer:  buffer.NewChannelBuffer[*Data[D, T]](ctx2, bufsize, 1),
		ctx:     ctx2,
		cancel:  cancel2,
	}

	if len(readers) > 0 {
		for _, reader := range readers {
			r.AddReader(reader)
		}
	}

	return r
}

// AddReader wraps the given Reader in a BufferedReader that writes into the
// shared buffer.
func (r *MultiReader2[D, T]) AddReader(reader Reader[D, T]) {
	if reader == nil {
		return
	}

	r.readers.Add(NewBufferedReaderWithBuffer(r.ctx, reader, r.buffer))
}

// RemoveReader stops the buffering loop for the specified Reader, without
// closing the shared buffer.
func (r *MultiReader2[D, T]) RemoveReader(reader Reader[D, T]) {
	if reader == nil {
		return
	}

	r.readers.RemoveIf(func(r2 *BufferedReader[D, T]) bool {
		if r2 == nil {
			return true // NOTE: REMOVE ANY INVALID READERS
		}

		if r2.reader == reader || r2 == reader {
			// NOTE: DOES NOT CLOSE BUFFER; ONLY STOPS THE READ BUFFER LOOP
			// NOTE: THIS IS INTENTIONAL; reader IS ASSUMED TO BE OWNED BY THE CALLER NOT MultiReader2
			// NOTE: ALL READERS CAN BE CLOSE MY MANUALLY CALLING Close ON MultiReader2
			r2.close()
			return true
		}

		return false
	})
}

// Read pops the next item from the shared buffer.
func (r *MultiReader2[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	// NOTE: THIS DOES NOT INTERACT WITH THE ACTUAL READERS; SO AUTO REMOVE WHEN ERROR DOES NOT WORK HERE
	return r.buffer.Pop(ctx)
}

func (r *MultiReader2[D, T]) Size() int {
	return r.readers.Size()
}

// Close stops all internal buffering loops, closes all wrapped Readers, and
// closes the shared buffer.
func (r *MultiReader2[D, T]) Close() error {
	var merr error

	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}

		for _, reader := range r.readers.Items() {
			merr = multierr.Append(merr, reader.Close())
		}

		r.buffer.Close()
	})

	return merr
}

// SwappableReader allows hot-swapping the underlying Reader at runtime. Swap
// closes the old Reader before installing the new one.
type SwappableReader[D, T any] struct {
	reader Reader[D, T]
	mux    sync.RWMutex
}

// NewSwappableReader constructs a SwappableReader around the provided Reader.
func NewSwappableReader[D, T any](reader Reader[D, T]) *SwappableReader[D, T] {
	return &SwappableReader[D, T]{
		reader: reader,
	}
}

// Swap replaces the current Reader with the provided one, closing the previous
// Reader atomically under a write lock.
func (r *SwappableReader[D, T]) Swap(reader Reader[D, T]) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if err := r.reader.Close(); err != nil {
		return fmt.Errorf("error while swapping reader (err: %v)", err)
	}

	r.reader = reader

	return nil
}

// Read delegates to the current underlying Reader under a read lock.
func (r *SwappableReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.reader.Read(ctx)
}

// Close closes the current underlying Reader under a write lock.
func (r *SwappableReader[D, T]) Close() error {
	r.mux.Lock()
	defer r.mux.Unlock()

	return r.reader.Close()
}

type CanWeightReader[D, T any] interface {
	Reader[D, T]
	Weight() int64
}

type WeightReader[D, T any] struct {
	Reader[D, T]
	f func() int64
}

func NewWeightReader[D, T any](writer Writer[D, T], f func() int64) WeightWriter[D, T] {
	return WeightWriter[D, T]{
		Writer: writer,
		f:      f,
	}
}

func (r *WeightReader[D, T]) Weight() int64 {
	return r.f()
}

type WeightedReader[D, T any] struct {
	readers *set.SafeSet[CanWeightReader[D, T]]

	// high determines the selection strategy:
	// true = pick the highest weight
	// false = pick the lowest weight
	high bool
}

func NewWeightedReader[D, T any](high bool, readers ...CanWeightReader[D, T]) *WeightedReader[D, T] {
	r := &WeightedReader[D, T]{
		readers: set.NewSafeSet[CanWeightReader[D, T]](),
		high:    high,
	}

	if len(readers) > 0 {
		for _, reader := range readers {
			r.AddReader(reader)
		}
	}

	return r
}

func (r *WeightedReader[D, T]) AddReader(reader Reader[D, T]) {
	if reader == nil {
		return
	}

	wr, ok := reader.(CanWeightReader[D, T])
	if !ok {
		fmt.Printf("weight reader: added reader should satisfy CanWeightReader[D, T] interface. ignoring...\n")
		return
	}

	r.readers.Add(wr)
}

func (r *WeightedReader[D, T]) RemoveReader(reader Reader[D, T]) {
	if reader == nil {
		return
	}

	r.readers.RemoveIf(func(wr CanWeightReader[D, T]) bool {
		if wr == nil && wr == reader {
			return true
		}
		return false
	})
}

func (r *WeightedReader[D, T]) Read(ctx context.Context) (*Data[D, T], error) {
	var (
		first               = true
		weight int64        = 0
		wr     Reader[D, T] = nil
	)

	for _, reader := range r.readers.Items() {
		cw := reader.Weight()

		if first {
			wr = reader
			weight = cw
			first = false
		}

		if (r.high && cw > weight) || (!r.high && cw < weight) {
			wr = reader
			weight = cw
		}
	}

	if wr == nil {
		// return nil, errors.New("weighted reader: no readers available")
		return nil, nil // returning error stops pipe
	}

	data, err := wr.Read(ctx)
	if err != nil {
		r.RemoveReader(wr)

		return r.Read(ctx)
	}

	return data, nil
}

func (r *WeightedReader[D, T]) Size() int {
	return r.readers.Size()
}

func (r *WeightedReader[D, T]) Close() error {
	var merr error = nil

	for _, reader := range r.readers.Items() {
		if err := reader.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	r.readers.Clear()

	return merr
}
