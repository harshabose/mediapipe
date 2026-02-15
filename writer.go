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

// Writer consumes `*Data[D, T]` units produced by a Reader. Implementations
// should extract the destination value D (typically via `Data.Get`) and send
// it to an external sink. Writers must honor context cancellation and deadlines
// and may return io.EOF to signal that no more data should be written.
type Writer[D, T any] interface {
	Write(context.Context, *Data[D, T]) error
	io.Closer
}

// CanAddWriter is implemented by composites that support adding another Writer
// at runtime.
type CanAddWriter[D, T any] interface {
	AddWriter(Writer[D, T])
}

// CanRemoveWriter is implemented by composites that support removing a Writer
// at runtime.
type CanRemoveWriter[D, T any] interface {
	RemoveWriter(Writer[D, T])
}

// CanConsume abstracts a sink that can accept values of type D.
type CanConsume[D any] interface {
	Consume(context.Context, D) error
}

type CanConsumeFunc[D any] func(context.Context, D) error

func (f CanConsumeFunc[D]) Generate(ctx context.Context, d D) error {
	return f(ctx, d)
}

// GeneralWriter adapts a `CanConsume[D]` into a `Writer[D, T]`. It typically
// calls `Get` on incoming `Data[D, T]` to obtain D and forwards it to the
// consumer. An optional transformer is reserved for future use.
type GeneralWriter[D, T any] struct {
	consumer    CanConsume[D]      // The destination that will consume the data
	transformer func(D) (T, error) // Optional transformer for future use
}

// NewGeneralWriter constructs a `GeneralWriter` with the given consumer.
func NewGeneralWriter[D, T any](consumer CanConsume[D], transformer func(D) (T, error)) *GeneralWriter[D, T] {
	return &GeneralWriter[D, T]{
		consumer:    consumer,
		transformer: transformer,
	}
}

// NewIdentityGeneralWriter constructs a `GeneralWriter` whose internal
// transformer is the identity function.
func NewIdentityGeneralWriter[D any](consumer CanConsume[D]) *GeneralWriter[D, D] {
	return &GeneralWriter[D, D]{
		consumer: consumer,
		transformer: func(d D) (D, error) {
			return d, nil
		},
	}
}

// Write extracts the destination value via `data.Get()` and forwards it to the
// underlying consumer. A nil `data` is a no-op.
func (w *GeneralWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	if data == nil {
		return nil
	}

	d, err := data.Get()
	if err != nil {
		return err
	}

	return w.consumer.Consume(ctx, d)
}

// Close implements io.Closer. GeneralWriter does not own resources and returns
// nil.
func (w *GeneralWriter[D, T]) Close() error {
	return nil
}

// TimeoutWriter decorates another Writer and enforces a per-call timeout on
// Write operations.
type TimeoutWriter[D, T any] struct {
	w       Writer[D, T]
	timeout time.Duration
}

// NewTimeoutWriter wraps an existing Writer with the given timeout.
func NewTimeoutWriter[D, T any](writer Writer[D, T], timeout time.Duration) *TimeoutWriter[D, T] {
	return &TimeoutWriter[D, T]{
		w:       writer,
		timeout: timeout,
	}
}

// Write invokes the wrapped Writer with a context that times out after the
// configured duration.
func (w *TimeoutWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	ctx2, cancel2 := context.WithTimeout(ctx, w.timeout)
	defer cancel2()

	return w.w.Write(ctx2, data)
}

// Close propagates Close to the wrapped Writer.
func (w *TimeoutWriter[D, T]) Close() error {
	return w.w.Close()
}

// BufferedWriter decouples production from consumption: writes are enqueued
// into an internal buffer and a background loop flushes them to the underlying
// Writer.
type BufferedWriter[D, T any] struct {
	writer Writer[D, T]
	buffer buffer.Buffer[*Data[D, T]]

	wg     sync.WaitGroup
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBufferedWriter creates a BufferedWriter backed by a channel buffer of the
// specified size.
func NewBufferedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize uint) *BufferedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedWriter[D, T]{
		writer: writer,
		buffer: buffer.NewChannelBuffer[*Data[D, T]](ctx2, bufsize, 1),
		ctx:    ctx2,
		cancel: cancel,
	}

	go w.loop()

	return w
}

func NewBufferedWriterWithBuffer[D, T any](ctx context.Context, writer Writer[D, T], buf buffer.Buffer[*Data[D, T]]) *BufferedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedWriter[D, T]{
		writer: writer,
		buffer: buf,
		ctx:    ctx2,
		cancel: cancel,
	}

	go w.loop()

	return w
}

// Write enqueues the data into the internal buffer for asynchronous flushing
// by the background loop.
func (w *BufferedWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	return w.buffer.Push(ctx, data)
}

func (w *BufferedWriter[D, T]) loop() {
	defer w.close()

	w.wg.Add(1)
	defer w.wg.Done()

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			data, err := w.buffer.Pop(w.ctx)
			if err != nil {
				// fmt.Printf("buffered writer error while popping from buffer; err: %v", err)
				return
			}

			if err := w.writer.Write(w.ctx, data); err != nil {
				// fmt.Printf("buffered writer error while writing to the writer; err: %v\n", err)
				return
			}
		}
	}
}

func (w *BufferedWriter[D, T]) close() {
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		// r.wg.Wait() // TODO: this is causing to wait for the writer who does not honour ctx; fix this asap

	})
}

// Close stops the background loop, closes the buffer, and then closes the
// wrapped Writer.
func (w *BufferedWriter[D, T]) Close() error {
	w.close()

	w.buffer.Close()

	if w.writer != nil {
		if err := w.writer.Close(); err != nil {
			return err
		}
	}

	return nil
}

// BufferedTimedWriter is similar to BufferedWriter, but flushes the buffer to
// the underlying Writer at fixed time intervals using a ticker.
type BufferedTimedWriter[D, T any] struct {
	writer   Writer[D, T]               // Direct reference to the underlying writer
	buffer   buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements
	interval time.Duration              // Time interval between writeAll operations

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background writing loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

// NewBufferedTimedWriter constructs a BufferedTimedWriter with the given buffer
// size and flush interval.
func NewBufferedTimedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize int, interval time.Duration) *BufferedTimedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedTimedWriter[D, T]{
		writer:   writer,
		buffer:   buffer.NewChannelBuffer[*Data[D, T]](ctx2, uint(bufsize), 1),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	go w.loop()

	return w
}

// Write enqueues the data into the internal buffer for periodic flushing.
func (w *BufferedTimedWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	return w.buffer.Push(ctx, data)
}

func (w *BufferedTimedWriter[D, T]) loop() {
	defer w.close()

	w.wg.Add(1)
	defer w.wg.Done()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			data, err := w.buffer.Pop(w.ctx)
			if err != nil {
				fmt.Printf("buffered timed writer error while popping from buffer; err: %v", err)
				return
			}

			if err := w.writer.Write(w.ctx, data); err != nil {
				fmt.Printf("buffered timed writer error while writing to the writer; err: %v\n", err)
				return
			}
		}
	}
}

func (w *BufferedTimedWriter[D, T]) close() {
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		w.wg.Wait()
	})
}

// Close stops the ticker loop, closes the buffer, and then closes the wrapped
// Writer.
func (w *BufferedTimedWriter[D, T]) Close() error {
	w.close()

	w.buffer.Close()

	if w.writer != nil {
		if err := w.writer.Close(); err != nil {
			return err
		}
	}

	return nil
}

// MultiWriter broadcasts each write to all underlying Writers. Writers that
// return an error are removed from the set.
type MultiWriter[D, T any] struct {
	writers *set.Set[Writer[D, T]]

	index int
	cond  *cond.ContextCond
	mux   sync.RWMutex
}

// NewMultiWriter constructs a MultiWriter with an optional initial set of
// Writers.
func NewMultiWriter[D, T any](writers ...Writer[D, T]) *MultiWriter[D, T] {
	w := &MultiWriter[D, T]{
		writers: set.NewSet(writers...),
		index:   0,
	}
	w.cond = cond.NewContextCond(&w.mux)

	return w
}

// Write forwards the data to each underlying Writer, aggregating errors. Any
// Writer that fails is removed.
func (w *MultiWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	writer, err := w.getWriters(ctx)
	if err != nil {
		return err
	}

	if err := writer.Write(ctx, data); err != nil {
		w.RemoveWriter(writer)
		return err
	}

	return nil
}

// getWriters blocks until at least one Writer is present and then returns a
// snapshot slice of current Writers.
func (w *MultiWriter[D, T]) getWriters(ctx context.Context) (Writer[D, T], error) {
	w.mux.Lock()
	defer w.mux.Unlock()

	for w.writers.Size() == 0 {
		if err := w.cond.Wait(ctx); err != nil {
			return nil, err
		}
	}

	if w.index >= w.writers.Size() {
		w.index = 0
	}

	writer := w.writers.Items()[w.index]
	w.index = (w.index + 1) % w.writers.Size()

	return writer, nil
}

// AddWriter adds a Writer to the broadcast set and signals waiting writers.
func (w *MultiWriter[D, T]) AddWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	w.writers.Add(writer)

	w.cond.Broadcast()
}

// RemoveWriter removes a Writer from the broadcast set.
func (w *MultiWriter[D, T]) RemoveWriter(writer Writer[D, T]) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if writer == nil {
		return
	}

	w.writers.Remove(writer)
}

// Close closes all Writers currently held by the MultiWriter and aggregates any
// errors.
func (w *MultiWriter[D, T]) Close() error {
	w.mux.Lock()
	defer w.mux.Unlock()

	var merr error

	for _, writer := range w.writers.Items() {
		if err := writer.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	return merr
}

// MultiWriter2 wraps each added Writer with a BufferedWriter (if not already)
// and fan-outs writes to all of them. It owns and stops the background loops
// for those buffered wrappers on Close.
type MultiWriter2[D, T any] struct {
	writers *set.SafeSet[*BufferedWriter[D, T]]
	bufsize uint

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewMultiWriter2 constructs a MultiWriter2 with a context for internal
// buffered writers and an optional initial set of Writers.
func NewMultiWriter2[D, T any](ctx context.Context, bufsize uint, writers ...Writer[D, T]) *MultiWriter2[D, T] {
	ctx2, cancel2 := context.WithCancel(ctx)

	w := &MultiWriter2[D, T]{
		writers: set.NewSafeSet[*BufferedWriter[D, T]](),
		bufsize: bufsize,
		ctx:     ctx2,
		cancel:  cancel2,
	}

	for _, writer := range writers {
		w.AddWriter(writer)
	}

	return w
}

// AddWriter ensures the given Writer has buffering and adds it to the set.
func (w *MultiWriter2[D, T]) AddWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}

	bufw, ok := writer.(*BufferedWriter[D, T])
	if !ok {
		bufw = NewBufferedWriter(w.ctx, writer, w.bufsize)
	}

	w.writers.Add(bufw)
}

// RemoveWriter removes the specified Writer (or its buffered wrapper) from the
// set.
func (w *MultiWriter2[D, T]) RemoveWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}

	w.writers.RemoveIf(func(w *BufferedWriter[D, T]) bool {
		if w == nil {
			return true // NOTE: REMOVE ANY INVALID WRITERS
		}

		return w.writer == writer || w == writer
	})
}

// Write forwards the data to each buffered Writer, aggregating errors and
// pruning failing writers.
func (w *MultiWriter2[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	for _, writer := range w.writers.Items() {
		if err := writer.Write(ctx, data); err != nil {
			fmt.Printf("error in multiwriter2: write error=%v. Removing error writer...\n", err)
			w.RemoveWriter(writer)
		}
	}

	return nil
}

func (w *MultiWriter2[D, T]) Size() int {
	return w.writers.Size()
}

// Close stops internal background loops and closes all buffered Writers.
func (w *MultiWriter2[D, T]) Close() error {
	var merr error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		for _, writer := range w.writers.Items() {
			merr = multierr.Append(merr, writer.Close())
		}
	})

	return merr
}

// SwappableWriter allows hot-swapping the underlying Writer at runtime. Swap
// closes the current Writer before installing the new one.
type SwappableWriter[D, T any] struct {
	writer Writer[D, T]
	mux    sync.RWMutex
}

// NewSwappableWriter constructs a SwappableWriter around the provided Writer.
func NewSwappableWriter[D, T any](writer Writer[D, T]) *SwappableWriter[D, T] {
	return &SwappableWriter[D, T]{
		writer: writer,
	}
}

// Get returns current active writer
func (w *SwappableWriter[D, T]) Get() Writer[D, T] {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.writer
}

// Swap replaces the current Writer with the provided one and returns the previous
func (w *SwappableWriter[D, T]) Swap(writer Writer[D, T]) Writer[D, T] {
	w.mux.Lock()
	defer w.mux.Unlock()

	old := w.writer
	w.writer = writer

	return old
}

func (w *SwappableWriter[D, T]) Hold(ctx context.Context, writer Writer[D, T]) {
	old := w.Swap(writer)
	defer w.Swap(old)

	<-ctx.Done()
}

// Write delegates to the current underlying Writer under a read lock.
func (w *SwappableWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.writer.Write(ctx, data)
}

// Close closes the current underlying Writer under a write lock.
func (w *SwappableWriter[D, T]) Close() error {
	w.mux.Lock()
	defer w.mux.Unlock()

	return w.writer.Close()
}

type CanWeightWriter[D, T any] interface {
	Writer[D, T]
	Weight() int64
}

type WeightWriter[D, T any] struct {
	Writer[D, T]
	f func() int64
}

func NewWeightWriter[D, T any](writer Writer[D, T], f func() int64) *WeightWriter[D, T] {
	return &WeightWriter[D, T]{
		Writer: writer,
		f:      f,
	}
}

func (w *WeightWriter[D, T]) Weight() int64 {
	return w.f()
}

type WeightedWriter[D, T any] struct {
	writers *set.SafeSet[CanWeightWriter[D, T]]

	// high determines the selection strategy:
	// true = pick the highest weight
	// false = pick the lowest weight
	high bool
}

func NewWeightedWriter[D, T any](high bool, writers ...Writer[D, T]) *WeightedWriter[D, T] {
	w := &WeightedWriter[D, T]{
		writers: set.NewSafeSet[CanWeightWriter[D, T]](),
		high:    high,
	}

	if len(writers) > 0 {
		for _, writer := range writers {
			w.AddWriter(writer)
		}
	}

	return w
}

func (w *WeightedWriter[D, T]) AddWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}

	ww, ok := writer.(CanWeightWriter[D, T])
	if !ok {
		fmt.Printf("weight writer: added writer should satisfy CanWeightWriter[D, T] interface. ignoring...\n")
		return
	}

	w.writers.Add(ww)
}

func (w *WeightedWriter[D, T]) RemoveWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}

	w.writers.RemoveIf(func(ww CanWeightWriter[D, T]) bool {
		return ww == writer
	})
}

func (w *WeightedWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	var (
		first        = true
		weight int64 = 0
		ww     CanWeightWriter[D, T]
	)

	for _, writer := range w.writers.Items() {
		cw := writer.Weight()

		if first {
			ww = writer
			weight = cw
			first = false
			continue
		}

		if (w.high && cw > weight) || (!w.high && cw < weight) {
			ww = writer
			weight = cw
		}
	}

	if ww == nil {
		// return errors.New("weighted writer: no writers available")
		return nil // returning error stops pipe
	}

	if err := ww.Write(ctx, data); err != nil {
		w.RemoveWriter(ww)

		return w.Write(ctx, data)
	}

	return nil
}

func (w *WeightedWriter[D, T]) Size() int {
	return w.writers.Size()
}

func (w *WeightedWriter[D, T]) Close() error {
	var merr error = nil

	for _, writer := range w.writers.Items() {
		if err := writer.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	w.writers.Clear()

	return merr
}

type StickyWeightedWriter[D, T any] struct {
	writers *set.SafeSet[CanWeightWriter[D, T]]

	current CanWeightWriter[D, T]
	mux     sync.RWMutex
	high    bool
}

func NewStickyWeightedWriter[D, T any](high bool, writers ...Writer[D, T]) *StickyWeightedWriter[D, T] {
	w := &StickyWeightedWriter[D, T]{
		writers: set.NewSafeSet[CanWeightWriter[D, T]](),
		high:    high,
	}

	if len(writers) > 0 {
		for _, writer := range writers {
			w.AddWriter(writer)
		}
	}

	return w
}

func (w *StickyWeightedWriter[D, T]) AddWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}
	ww, ok := writer.(CanWeightWriter[D, T])
	if !ok {
		fmt.Printf("sticky weight writer: added writer should satisfy CanWeightWriter[D, T] interface. ignoring...\n")
		return
	}

	w.writers.Add(ww)

	w.mux.Lock()
	if w.current == nil {
		w.current = ww
	}
	w.mux.Unlock()
}

func (w *StickyWeightedWriter[D, T]) RemoveWriter(writer Writer[D, T]) {
	if writer == nil {
		return
	}

	w.writers.RemoveIf(func(ww CanWeightWriter[D, T]) bool {
		return ww == writer
	})

	w.mux.Lock()
	defer w.mux.Unlock()

	if w.current == writer {
		w.current = nil
	}
}

func (w *StickyWeightedWriter[D, T]) Write(ctx context.Context, data *Data[D, T]) error {
	active, err := w.get()
	if err != nil {
		return err
	}

	if active == nil {
		return nil
	}

	if err := active.Write(ctx, data); err != nil {
		w.RemoveWriter(active)

		return w.Write(ctx, data)
	}

	return nil
}

func (w *StickyWeightedWriter[D, T]) get() (CanWeightWriter[D, T], error) {
	if current := w.get2(); current != nil {
		return current, nil
	}

	return w.set()
}

func (w *StickyWeightedWriter[D, T]) get2() CanWeightWriter[D, T] {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.current
}

func (w *StickyWeightedWriter[D, T]) set() (CanWeightWriter[D, T], error) {
	w.mux.Lock()
	defer w.mux.Unlock()

	if w.current != nil {
		return w.current, nil
	}

	var (
		first  = true
		ww     CanWeightWriter[D, T]
		weight int64 = 0
	)

	for _, writer := range w.writers.Items() {
		cw := writer.Weight()

		if first {
			ww = writer
			weight = cw
			first = false
			continue
		}

		if (w.high && cw > weight) || (!w.high && cw < weight) {
			ww = writer
			weight = cw
		}
	}

	if ww == nil {
		// return nil, errors.New("sticky writer: no writers available")
		return nil, nil // returning error stops pipe
	}

	w.current = ww
	return ww, nil
}

func (w *StickyWeightedWriter[D, T]) Close() error {
	var merr error = nil

	for _, writer := range w.writers.Items() {
		if err := writer.Close(); err != nil {
			merr = multierr.Append(merr, err)
		}
	}

	w.writers.Clear()

	return merr
}
