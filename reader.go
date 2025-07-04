package mediasink

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/harshabose/tools/buffer/pkg"
)

// Reader defines the interface for reading Data elements with proper resource cleanup.
// This interface abstracts different data sources and makes them compatible with
// the buffered reading system.
//
// Type parameters:
//   - D: The target type that read Data elements can be transformed into
//   - T: The source type used internally by the reader
//
// Any type implementing this interface can be used with BufferedReader and
// BufferedTimedReader, enabling a uniform interface for diverse data sources.
type Reader[D, T any] interface {
	Read() (*Data[D, T], error)
	io.Closer
}

// CanGenerate represents any data source that can produce data of type T.
// This is the most basic interface for data generators and provides the foundation
// for the universal reader system.
//
// Sources implementing this interface can be:
//   - WebRTC tracks producing RTP packets
//   - File readers producing byte streams
//   - Network connections receiving protocol data
//   - Hardware sensors generating telemetry
//   - Audio/video encoders producing compressed streams
//
// The universal reader system (AnyReader) adapts any CanGenerate[T] source
// into a AnyReader[D, T] that produces transformable Data elements.
type CanGenerate[T any] interface {
	// Generate returns the next piece of data of type T or an error if reading fails.
	// This method should block until data is available or an error occurs.
	Generate() (T, error)
}

// AnyReader is the universal adapter that converts any CanGenerate[T] source into
// a Reader[D, T] by wrapping each piece of source data with a transformation function.
//
// This is the key component that bridges the gap between raw data sources and the
// Data transformation system. It allows any data generator to participate in the
// universal media routing pipeline.
//
// Type parameters:
//   - D: The target type that produced Data elements can be transformed into
//   - T: The source type produced by the generator
//
// The AnyReader applies the transformer function to each piece of data read from
// the generator, creating Data[D] elements that can be buffered and consumed
// by different output systems.
type AnyReader[D, T any] struct {
	generator   CanGenerate[T]     // The source data generator
	transformer func(T) (D, error) // Function to transform T into D
}

// NewAnyReader creates a universal reader with an explicit transformer function.
//
// This constructor is used when the source type T and target type D are different,
// requiring an actual transformation. The transformer function defines how to
// convert from the source format to the target format.
//
// Parameters:
//   - generator: Any source that can produce data of type T
//   - transformer: Function that converts T to D for the target consumer
//
// Example:
//
//	// RTP packets from WebRTC to bytes for UDP
//	reader := NewAnyReader(webrtcTrack, func(p *rtp.Packet) ([]byte, error) {
//	    return p.Marshal()
//	})
//
//	// MAVLINK data with custom framing for specific protocol
//	reader := NewAnyReader(mavlinkSource, func(data []byte) ([]byte, error) {
//	    return addCustomFraming(data)
//	})
func NewAnyReader[D, T any](generator CanGenerate[T], transformer func(T) (D, error)) *AnyReader[D, T] {
	return &AnyReader[D, T]{
		generator:   generator,
		transformer: transformer,
	}
}

// NewIdentityAnyReader creates a reader for cases where source and target types are identical.
//
// This convenience constructor is used when no transformation is needed - the source
// type T is the same as the target type D. It automatically provides an identity
// transformation function that returns the data unchanged.
//
// This is common when:
//   - Reading RTP packets that will be used directly as RTP packets
//   - Reading bytes that will be transmitted as bytes
//   - Reading any data type that matches the consumer's expected type
//
// Parameters:
//   - generator: Any source that produces data of type T
//
// Example:
//
//	// RTP packets that will be used directly as RTP packets
//	reader := NewIdentityAnyReader(rtpSource)
//
//	// Byte data that will be transmitted directly as bytes
//	reader := NewIdentityAnyReader(byteSource)
func NewIdentityAnyReader[T any](generator CanGenerate[T]) *AnyReader[T, T] {
	return &AnyReader[T, T]{
		generator: generator,
		transformer: func(t T) (T, error) {
			return t, nil // Identity transformation
		},
	}
}

// Read gets the next piece of data from the generator and wraps it as a Data element
// using the configured transformer function.
//
// This method serves as the bridge between raw data generation and the Data transformation
// system. Each call:
//  1. Reads raw data T from the generator
//  2. Wraps it with the transformer function to create Data[D]
//  3. Returns the Data element for buffering and consumption
//
// The actual transformation from T to D is deferred until a consumer calls Get()
// on the returned Data element, enabling lazy evaluation and memory efficiency.
func (r *AnyReader[D, T]) Read() (*Data[D, T], error) {
	t, err := r.generator.Generate()
	if err != nil {
		return nil, err
	}

	e := Wrap[D, T](t, r.transformer)
	return e, nil
}

// Close implements io.Closer for resource cleanup.
// Currently, this is a no-op as AnyReader doesn't manage generator lifecycle.
// Generator cleanup should be handled by the code that created the generator.
func (r *AnyReader[D, T]) Close() error {
	return nil
}

// BufferedReader wraps a AnyReader[D, T] and provides asynchronous buffering of Data elements.
// It continuously reads from the underlying reader in a background goroutine and stores
// Data elements in an internal buffer, allowing consumers to read at their own pace.
//
// The buffering system decouples data production speed from consumption speed, which is
// critical in streaming media applications where:
//   - Network sources may produce data in bursts
//   - Different consumers may read at different rates
//   - Temporary processing delays shouldn't cause data loss
//   - Multiple consumers may share the same buffered stream
//
// Type parameters:
//   - D: The target type that buffered Data elements can be transformed into
//   - T: The source type used by the underlying reader
//
// The BufferedReader maintains the Data[D] abstraction, so consumers still get
// transformable elements that can be converted to type D when needed.
type BufferedReader[D, T any] struct {
	reader Reader[D, T]               // Direct reference to the reader
	buffer buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background reading loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

// NewBufferedReader creates a new BufferedReader that continuously reads Data elements
// from the provided reader in a background goroutine.
//
// The buffering starts immediately upon creation, with a dedicated goroutine continuously
// reading from the source reader and storing Data elements in the internal buffer.
// This ensures that data is captured even when consumers are temporarily slow or unavailable.
//
// Parameters:
//   - ctx: Parent context for cancellation and timeouts. When cancelled, stops background reading.
//   - reader: The AnyReader to buffer data from
//   - bufsize: Maximum number of Data elements to buffer. When full, further reads may block.
//
// The background reading loop will exit when:
//   - The context is cancelled
//   - An error occurs reading from the underlying reader
//   - The reader reaches end-of-stream
//
// Example:
//
//	// Buffer RTP packets from WebRTC track
//	reader := NewReader(webrtcTrack, rtpTransformer)
//	buffered := NewBufferedReader(ctx, reader, 100)
//	defer buffered.Close()
//
//	// Non-blocking reads from buffer
//	data, err := buffered.Generate()
//	packet, err := data.Get() // Transform when needed
func NewBufferedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize int) *BufferedReader[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	r := &BufferedReader[D, T]{
		reader: reader,
		buffer: buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		ctx:    ctx2,
		cancel: cancel,
	}

	r.wg.Add(1)
	go r.loop()

	return r
}

// Read returns the next buffered Data element. This method reads from the internal buffer
// rather than directly from the underlying reader, making it non-blocking as long as
// there are Data elements in the buffer.
//
// The returned Data[D] element can be transformed to type D using its Get() method,
// maintaining the lazy transformation pattern even through the buffering layer.
//
// Returns an error if:
//   - The context is cancelled
//   - The buffer is closed (usually due to reader error or end-of-stream)
//   - Buffer operations fail
func (r *BufferedReader[D, T]) Read() (*Data[D, T], error) {
	return r.buffer.Pop(r.ctx)
}

// loop runs in a background goroutine and continuously reads Data elements from the
// underlying reader, pushing them into the internal buffer.
//
// This method implements the asynchronous buffering behavior:
//  1. Continuously reads from the underlying reader
//  2. Pushes Data elements into the buffer for consumer access
//  3. Handles context cancellation for clean shutdown
//  4. Logs errors and exits on read failures
//
// The loop exits when:
//   - Context is cancelled (clean shutdown)
//   - Buffer push fails (logs error and exits)
//
// Individual reader errors are logged but don't stop the loop, allowing the system
// to be resilient to temporary network or source issues.
//
// This method should not be called directly; it's automatically started by NewBufferedReader.
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

// Close stops the background reading loop and cleans up resources.
// After calling Close, no more data will be read from the underlying reader,
// but existing buffered data can still be consumed until the buffer is empty.
//
// It's safe to call Close multiple times. This method implements io.Closer
// for consistent resource management patterns.
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

// BufferedTimedReader extends BufferedReader with time-interval-based reading instead
// of continuous reading. It reads Data elements from the underlying reader at specified
// intervals, providing rate control for the data pipeline.
//
// This is particularly useful for:
//   - Rate-limiting data consumption from fast sources
//   - Periodic polling of sources that don't need continuous reading
//   - Controlling resource usage in high-throughput scenarios
//   - Implementing frame rate control for video streams
//   - Throttling telemetry data collection
//
// Type parameters:
//   - D: The target type that buffered Data elements can be transformed into
//   - T: The source type used by the underlying reader
//
// The timed reading behavior completely replaces the continuous reading of BufferedReader,
// using a ticker to control when reads occur from the underlying source.
type BufferedTimedReader[D, T any] struct {
	reader   Reader[D, T]               // Direct reference to the reader
	buffer   buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements
	interval time.Duration              // Time interval between read operations

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background reading loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

// NewBufferedTimedReader creates a new BufferedTimedReader that reads Data elements
// at specified time intervals instead of continuously.
//
// Unlike BufferedReader which reads as fast as possible, this reader uses a ticker
// to control the reading rate. Each tick triggers one read from the underlying reader,
// allowing precise control over data consumption rate.
//
// Parameters:
//   - ctx: Parent context for cancellation and timeouts
//   - reader: The AnyReader to buffer data from
//   - bufsize: Maximum number of Data elements to buffer
//   - interval: Time duration between read operations
//
// Use cases:
//   - Video streaming at specific frame rates (e.g., 33ms for 30fps)
//   - Throttled telemetry collection (e.g., every 100ms)
//   - Rate-limited data processing to prevent resource exhaustion
//   - Periodic sampling of high-frequency data sources
//
// Example:
//
//	// Generate video frames at 30fps (33ms intervals)
//	reader := NewReader(videoSource, frameTransformer)
//	timedReader := NewBufferedTimedReader(ctx, reader, 50, 33*time.Millisecond)
//	defer timedReader.Close()
//
//	// Generate MAVLINK telemetry every 100ms
//	mavReader := NewReader(mavlinkSource, telemetryTransformer)
//	timedReader := NewBufferedTimedReader(ctx, mavReader, 20, 100*time.Millisecond)
func NewBufferedTimedReader[D, T any](ctx context.Context, reader Reader[D, T], bufsize int, interval time.Duration) *BufferedTimedReader[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	r := &BufferedTimedReader[D, T]{
		reader:   reader,
		buffer:   buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	r.wg.Add(1)
	go r.loop()

	return r
}

// Read returns the next buffered Data element. This method reads from the internal buffer
// rather than directly from the underlying reader, making it non-blocking as long as
// there are Data elements in the buffer.
//
// The returned Data[D] element can be transformed to type D using its Get() method,
// maintaining the lazy transformation pattern even through the buffering layer.
//
// Returns an error if:
//   - The context is cancelled
//   - The buffer is closed (usually due to reader error or end-of-stream)
//   - Buffer operations fail
func (r *BufferedTimedReader[D, T]) Read() (*Data[D, T], error) {
	return r.buffer.Pop(r.ctx)
}

// loop runs in a background goroutine and reads Data elements from the underlying reader
// at the specified time intervals using a ticker for precise timing control.
//
// This method implements the time-controlled reading behavior:
//  1. Creates a ticker with the specified interval
//  2. Reads from the underlying reader only on ticker events
//  3. Pushes Data elements into the buffer for consumer access
//  4. Handles context cancellation for clean shutdown
//
// The timed approach ensures predictable reading rates and prevents overwhelming
// consumers or sources with too-frequent operations.
//
// The loop exits when:
//   - Context is cancelled (clean shutdown with ticker cleanup)
//   - Buffer push fails (logs error and exits)
//
// Individual reader errors are logged but don't stop the loop, allowing the system
// to be resilient to temporary network or source issues.
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

// Close stops the background reading loop and cleans up resources.
// After calling Close, no more data will be read from the underlying reader,
// but existing buffered data can still be consumed until the buffer is empty.
//
// It's safe to call Close multiple times. This method implements io.Closer
// for consistent resource management patterns.
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
