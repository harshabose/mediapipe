// Writer System - The Output Side of the Universal Media Router
//
// This file implements the writeAll/output side of the universal media routing system,
// providing the symmetric counterpart to the Reader system. While Readers handle
// data ingestion and transformation, Writers handle data consumption and output
// to various destinations.
//
// # The Writer Pattern
//
// The Writer system follows the same lazy transformation philosophy as the Reader
// system but in reverse - it takes Data[D, T] elements from the pipeline and
// transforms them for consumption by various output destinations:
//
//   - WebRTC Data → RTP packets → UDP sockets
//   - MAVLINK Data → WebSocket frames → Mission Planner
//   - Audio Data → compressed streams → RTSP servers
//   - Any Data → any format → any destination
//
// # Bidirectional Architecture
//
// Combined with the Reader system, this enables complete bidirectional pipelines:
//
//   Input: CanGenerate → Reader → BufferedReader → Data[D,T]
//   Output: Data[D,T] → BufferedWriter → Writer → CanConsume
//
// This allows building complex media routing topologies where any source can
// feed any destination through the same unified pipeline.

package mediapipe

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/harshabose/tools/buffer/pkg"
)

// Writer defines the interface for writing Data elements to output destinations.
// This is the output counterpart to the Reader interface, completing the
// bidirectional nature of the universal media routing system.
//
// Type parameters:
//   - D: The target type that Data elements can be transformed into
//   - T: The source type contained within the Data elements
//
// Writers handle the final stage of the pipeline where Data elements are
// transformed and sent to their ultimate destinations.
type Writer[D, T any] interface {
	// Write transforms and sends a Data element to the output destination.
	// The Data element's Get() method is called to perform the transformation
	// before the result is consumed by the underlying destination.
	Write(*Data[D, T]) error
	io.Closer
}

// CanConsume represents any output destination that can accept data of type D.
// This is the output counterpart to CanGenerate, defining the basic interface
// for data consumption endpoints.
//
// Destinations implementing this interface can be:
//   - UDP sockets consuming byte streams
//   - WebSocket connections consuming framed data
//   - RTSP servers consuming RTP packets
//   - TCP connections consuming protocol-specific data
//   - File writers consuming serialized data
//   - Database writers consuming structured data
//
// The AnyWriter system adapts any CanConsume[D] destination into a Writer[D, T]
// that can participate in the universal media routing pipeline.
type CanConsume[D any] interface {
	// Consume accepts and processes data of type D.
	// This method should handle the data according to the destination's
	// specific requirements (network transmission, file writing, etc.).
	Consume(D) error
}

// AnyWriter is the universal adapter that converts any CanConsume[D] destination into
// a Writer[D, T] by extracting data from Data elements and optionally transforming it.
//
// This is the output counterpart to AnyReader, providing the bridge between the
// Data transformation system and raw output destinations. It allows any destination
// to participate in the universal media routing pipeline.
//
// Type parameters:
//   - D: The type that the consumer expects to receive
//   - T: The source type contained within incoming Data elements
//
// Note: The transformer is currently unused but included for future extensibility
// where additional transformations might be needed before consumption.
type AnyWriter[D, T any] struct {
	consumer    CanConsume[D]      // The destination that will consume the data
	transformer func(D) (T, error) // Optional transformer for future use
}

// NewAnyWriter creates a universal writer with an explicit transformer function.
//
// This constructor is designed for future extensibility where additional
// transformations might be needed between Data.Get() and consumer.Consume().
// Currently, the transformer is not used in the Write path.
//
// Parameters:
//   - consumer: Any destination that can consume data of type D
//   - transformer: Function for potential future transformations (currently unused)
//
// Example:
//
//	// UDP socket consuming byte streams
//	udpConsumer := &UDPSocket{}
//	writer := NewAnyWriter(udpConsumer, identityTransformer)
//
//	// WebSocket consuming framed data
//	wsConsumer := &WebSocketConnection{}
//	writer := NewAnyWriter(wsConsumer, framingTransformer)
func NewAnyWriter[D, T any](consumer CanConsume[D], transformer func(D) (T, error)) *AnyWriter[D, T] {
	return &AnyWriter[D, T]{
		consumer:    consumer,
		transformer: transformer,
	}
}

// NewIdentityAnyWriter creates a writer for cases where no additional transformation is needed.
//
// This convenience constructor is used when the Data element's transformation
// via Get() is sufficient and no additional processing is required before consumption.
// This is the most common case in the current system.
//
// Parameters:
//   - consumer: Any destination that can consume data of type D
//
// Example:
//
//	// Direct consumption without additional transformation
//	udpSocket := &UDPSocket{}
//	writer := NewIdentityAnyWriter(udpSocket)
//
//	// RTSP server consuming RTP packets directly
//	rtspServer := &RTSPServer{}
//	writer := NewIdentityAnyWriter(rtspServer)
func NewIdentityAnyWriter[D any](consumer CanConsume[D]) *AnyWriter[D, D] {
	return &AnyWriter[D, D]{
		consumer: consumer,
		transformer: func(d D) (D, error) {
			return d, nil
		},
	}
}

// Write extracts data from the Data element and sends it to the consumer.
//
// This method implements the Writer interface and serves as the bridge between
// the Data transformation system and raw output destinations. Each call:
//  1. Calls Get() on the Data element to perform the lazy transformation
//  2. Sends the transformed data to the consumer via Consume()
//
// The transformation from the source type T to target type D happens lazily
// when Get() is called, maintaining the efficiency benefits of the lazy
// evaluation system throughout the entire pipeline.
func (w *AnyWriter[D, T]) Write(data *Data[D, T]) error {
	d, err := data.Get()
	if err != nil {
		return err
	}

	return w.consumer.Consume(d)
}

// Close implements io.Closer for resource cleanup.
// Currently, this is a no-op as AnyWriter doesn't manage consumer lifecycle.
// Consumer cleanup should be handled by the code that created the consumer.
func (w *AnyWriter[D, T]) Close() error {
	return nil
}

// BufferedWriter wraps a Writer[D, T] and provides asynchronous buffering of Data elements
// for output. It continuously writes buffered Data elements to the underlying writer
// in a background goroutine, allowing producers to writeAll at their own pace.
//
// The buffering system decouples data production speed from consumption speed on the
// output side, which is critical in streaming media applications where:
//   - Network destinations may have variable latency
//   - Different output protocols may have different throughput characteristics
//   - Temporary network issues shouldn't block the entire pipeline
//   - Multiple writers may need to drain from the same source at different rates
//
// Type parameters:
//   - D: The target type that buffered Data elements can be transformed into
//   - T: The source type contained within the Data elements
//
// The BufferedWriter maintains the Data[D, T] abstraction, so the lazy transformation
// pattern is preserved even through the buffering layer.
type BufferedWriter[D, T any] struct {
	writer Writer[D, T]               // Direct reference to the underlying writer
	buffer buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background writing loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

// NewBufferedWriter creates a new BufferedWriter that continuously writes Data elements
// to the provided writer in a background goroutine.
//
// The writing starts immediately upon creation, with a dedicated goroutine continuously
// draining Data elements from the internal buffer and sending them to the underlying writer.
// This ensures that the pipeline can continue flowing even when individual writes
// experience temporary delays.
//
// Parameters:
//   - ctx: Parent context for cancellation and timeouts. When cancelled, stops background writing.
//   - writer: The Writer to send buffered data to
//   - bufsize: Maximum number of Data elements to buffer. When full, further writes may block.
//
// The background writing loop will exit when:
//   - The context is cancelled
//   - An error occurs reading from the buffer
//   - The buffer is closed
//
// Example:
//
//	// Buffer writes to UDP socket
//	udpWriter := NewAnyWriter(udpSocket, transformer)
//	buffered := NewBufferedWriter(ctx, udpWriter, 100)
//	defer buffered.Close()
//
//	// Non-blocking writes to buffer
//	err := buffered.Write(data) // Queues for background writing
func NewBufferedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize int) *BufferedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedWriter[D, T]{
		writer: writer,
		buffer: buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		ctx:    ctx2,
		cancel: cancel,
	}

	w.wg.Add(1)
	go w.loop()

	return w
}

// Write queues a Data element for asynchronous writing. This method adds the Data
// element to the internal buffer rather than writing directly to the underlying writer,
// making it non-blocking as long as there's space in the buffer.
//
// The actual transformation via Get() and writing via the underlying Writer happens
// in the background goroutine, maintaining the lazy evaluation pattern while
// providing asynchronous output capabilities.
//
// Returns an error if:
//   - The context is cancelled
//   - The buffer is full and the push times out
//   - The buffer is closed
func (w *BufferedWriter[D, T]) Write(data *Data[D, T]) error {
	return w.buffer.Push(w.ctx, data)
}

// loop runs in a background goroutine and continuously drains Data elements from the
// buffer, writing them to the underlying writer.
//
// This method implements the asynchronous writing behavior:
//  1. Continuously pops Data elements from the buffer
//  2. Writes them to the underlying writer via Write()
//  3. Handles context cancellation for clean shutdown
//  4. Logs errors but continues processing (non-fatal error handling)
//
// The loop exits when:
//   - Context is cancelled (clean shutdown)
//   - Buffer pop returns an error (usually indicating buffer closure)
//
// Individual writeAll errors are logged but don't stop the loop, allowing the system
// to be resilient to temporary network or destination issues.
//
// This method should not be called directly; it's automatically started by NewBufferedWriter.
func (w *BufferedWriter[D, T]) loop() {
	defer w.Close()
	defer w.wg.Done()

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			data, err := w.buffer.Pop(w.ctx)
			if err != nil {
				fmt.Printf("buffered writer error while popping from buffer; err: %v", err)
				return
			}

			if err := w.writer.Write(data); err != nil {
				fmt.Printf("buffered writer error while writing to the writer; err: %v", err)
				return
			}
		}
	}
}

// Close stops the background writing loop and cleans up resources.
// After calling Close, no more data will be written to the underlying writer,
// but existing buffered data will be processed until the buffer is empty or
// the context times out.
//
// It's safe to call Close multiple times. This method implements io.Closer
// for consistent resource management patterns.
func (w *BufferedWriter[D, T]) Close() error {
	var err error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		if w.writer != nil {
			if err = w.writer.Close(); err != nil {
				return
			}
		}

		w.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}

// BufferedTimedWriter extends BufferedWriter with time-interval-based writing instead
// of continuous writing. It writes Data elements to the underlying writer at specified
// intervals, providing rate control for the output pipeline.
//
// This is particularly useful for:
//   - Rate-limiting output to prevent overwhelming destinations
//   - Batching writes for efficiency (e.g., network protocols that benefit from batching)
//   - Controlling resource usage in high-throughput scenarios
//   - Implementing periodic transmission schedules
//   - Throttling output to match downstream processing capabilities
//
// Type parameters:
//   - D: The target type that buffered Data elements can be transformed into
//   - T: The source type contained within the Data elements
//
// The timed writing behavior replaces the continuous writing of BufferedWriter,
// using a ticker to control when writes occur to the underlying destination.
type BufferedTimedWriter[D, T any] struct {
	writer   Writer[D, T]               // Direct reference to the underlying writer
	buffer   buffer.Buffer[*Data[D, T]] // Internal buffer for storing Data elements
	interval time.Duration              // Time interval between writeAll operations

	ctx    context.Context    // Context for cancellation and cleanup
	cancel context.CancelFunc // Function to cancel the background writing loop
	wg     sync.WaitGroup     // WaitGroup for goroutine management
	once   sync.Once
}

// NewBufferedTimedWriter creates a new BufferedTimedWriter that writes Data elements
// at specified time intervals instead of continuously.
//
// Unlike BufferedWriter which writes as fast as possible, this writer uses a ticker
// to control the writing rate. Each tick triggers one writeAll to the underlying writer,
// allowing precise control over output data rates.
//
// Parameters:
//   - ctx: Parent context for cancellation and timeouts
//   - writer: The Writer to send buffered data to
//   - bufsize: Maximum number of Data elements to buffer
//   - interval: Time duration between writeAll operations
//
// Use cases:
//   - Network protocols with specific timing requirements
//   - Rate-limited APIs that have throughput restrictions
//   - Batch processing systems that benefit from periodic writes
//   - Output throttling to prevent overwhelming downstream systems
//
// Example:
//
//	// Write to WebSocket every 50ms for smooth streaming
//	wsWriter := NewAnyWriter(webSocket, transformer)
//	timedWriter := NewBufferedTimedWriter(ctx, wsWriter, 50, 50*time.Millisecond)
//	defer timedWriter.Close()
//
//	// Batch writes to database every 5 seconds
//	dbWriter := NewAnyWriter(database, transformer)
//	batchWriter := NewBufferedTimedWriter(ctx, dbWriter, 1000, 5*time.Second)
func NewBufferedTimedWriter[D, T any](ctx context.Context, writer Writer[D, T], bufsize int, interval time.Duration) *BufferedTimedWriter[D, T] {
	ctx2, cancel := context.WithCancel(ctx)
	w := &BufferedTimedWriter[D, T]{
		writer:   writer,
		buffer:   buffer.CreateChannelBuffer[*Data[D, T]](ctx2, bufsize, nil),
		interval: interval,
		ctx:      ctx2,
		cancel:   cancel,
	}

	w.wg.Add(1)
	go w.loop()

	return w
}

// Write queues a Data element for timed writing. This method adds the Data element
// to the internal buffer rather than writing directly to the underlying writer,
// making it non-blocking as long as there's space in the buffer.
//
// The actual writing happens at the specified intervals in the background goroutine,
// allowing for controlled output rates while maintaining the lazy evaluation pattern.
//
// Returns an error if:
//   - The context is cancelled
//   - The buffer is full and the push times out
//   - The buffer is closed
func (w *BufferedTimedWriter[D, T]) Write(data *Data[D, T]) error {
	return w.buffer.Push(w.ctx, data)
}

// loop runs in a background goroutine and writes Data elements to the underlying writer
// at the specified time intervals using a ticker for precise timing control.
//
// This method implements the time-controlled writing behavior:
//  1. Creates a ticker with the specified interval
//  2. Writes to the underlying writer only on ticker events
//  3. Pops one Data element from the buffer per tick
//  4. Handles context cancellation for clean shutdown
//
// The timed approach ensures predictable output rates and prevents overwhelming
// destinations with too-frequent writes. If the buffer is empty during a tick,
// the writeAll attempt will block until data is available or the context is cancelled.
//
// The loop exits when:
//   - Context is cancelled (clean shutdown with ticker cleanup)
//   - Buffer pop returns an error (usually indicating buffer closure)
//
// Individual writeAll errors are logged but don't stop the loop, maintaining
// resilience to temporary destination issues.
func (w *BufferedTimedWriter[D, T]) loop() {
	defer w.Close()
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

			if err := w.writer.Write(data); err != nil {
				fmt.Printf("buffered timed writer error while writing to the writer; err: %v", err)
				return
			}
		}
	}
}

// Close stops the background writing loop and cleans up resources.
// After calling Close, no more data will be written to the underlying writer,
// but existing buffered data will be processed until the buffer is empty,
// the ticker expires, or the context times out.
//
// It's safe to call Close multiple times. This method implements io.Closer
// for consistent resource management patterns.
func (w *BufferedTimedWriter[D, T]) Close() error {
	var err error

	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}

		if w.writer != nil {
			if err = w.writer.Close(); err != nil {
				return
			}
		}

		w.wg.Wait()
	})

	// TODO: ADD CLOSE ON BUFFER
	return nil
}
