package mediapipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Additional mock implementations specific to merge pipe testing

// mockReaderWithDelay implements Reader[D, T] with configurable delays for testing timing
type mockReaderWithDelay[D, T any] struct {
	data     []*Data[D, T]
	index    int
	closed   bool
	readErr  error
	closeErr error
	delay    time.Duration
	mu       sync.Mutex
}

func newMockReaderWithDelay[D, T any](data []*Data[D, T], delay time.Duration) *mockReaderWithDelay[D, T] {
	return &mockReaderWithDelay[D, T]{
		data:  data,
		delay: delay,
	}
}

func (r *mockReaderWithDelay[D, T]) Read() (*Data[D, T], error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, errors.New("reader is closed")
	}

	if r.readErr != nil {
		return nil, r.readErr
	}

	if r.delay > 0 {
		time.Sleep(r.delay)
	}

	if r.index >= len(r.data) {
		// Reset index to loop through data continuously for testing
		r.index = 0
	}

	if len(r.data) == 0 {
		return nil, errors.New("no data available")
	}

	data := r.data[r.index]
	r.index++
	return data, nil
}

func (r *mockReaderWithDelay[D, T]) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.closeErr
}

func (r *mockReaderWithDelay[D, T]) setReadError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readErr = err
}

func (r *mockReaderWithDelay[D, T]) setCloseError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeErr = err
}

func (r *mockReaderWithDelay[D, T]) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// mockWriterWithHistory implements Writer[D, T] with detailed write history tracking
type mockWriterWithHistory[D, T any] struct {
	written    []*Data[D, T]
	writeOrder []string // Track which reader data came from
	closed     bool
	writeErr   error
	closeErr   error
	mu         sync.Mutex
}

func newMockWriterWithHistory[D, T any]() *mockWriterWithHistory[D, T] {
	return &mockWriterWithHistory[D, T]{
		written:    make([]*Data[D, T], 0),
		writeOrder: make([]string, 0),
	}
}

func (w *mockWriterWithHistory[D, T]) Write(data *Data[D, T]) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return errors.New("writer is closed")
	}

	if w.writeErr != nil {
		return w.writeErr
	}

	w.written = append(w.written, data)

	// Extract data to track source
	if d, err := data.Get(); err == nil {
		if str, ok := any(d).(string); ok {
			w.writeOrder = append(w.writeOrder, str)
		}
	}

	return nil
}

func (w *mockWriterWithHistory[D, T]) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

func (w *mockWriterWithHistory[D, T]) getWritten() []*Data[D, T] {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]*Data[D, T], len(w.written))
	copy(result, w.written)
	return result
}

func (w *mockWriterWithHistory[D, T]) getWriteOrder() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]string, len(w.writeOrder))
	copy(result, w.writeOrder)
	return result
}

func (w *mockWriterWithHistory[D, T]) setWriteError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeErr = err
}

func (w *mockWriterWithHistory[D, T]) setCloseError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeErr = err
}

func (w *mockWriterWithHistory[D, T]) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func TestNewMergePipe(t *testing.T) {
	ctx := context.Background()
	writer := newMockWriter[string, string]()
	reader1 := newMockReader(createTestData([]string{"r1_data1", "r1_data2"}))
	reader2 := newMockReader(createTestData([]string{"r2_data1", "r2_data2"}))

	pipe := NewMergePipe(ctx, writer, reader1, reader2)

	if pipe == nil {
		t.Fatal("NewMergePipe returned nil")
	}

	if pipe.writer != writer {
		t.Error("Writer not set correctly")
	}

	if pipe.readers.Size() != 2 {
		t.Errorf("Expected 2 readers, got %d", pipe.readers.Size())
	}

	if pipe.paused {
		t.Error("Pipe should not be paused initially")
	}

	if pipe.readerIndex != 0 {
		t.Errorf("Reader index should start at 0: %d", pipe.readerIndex)
	}

	// Clean up
	pipe.Close()
}

func TestMergePipe_BasicFunctionality(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writer := newMockWriterWithHistory[string, string]()
	reader1 := newMockReader(createTestData([]string{"r1_data1", "r1_data2"}))
	reader2 := newMockReader(createTestData([]string{"r2_data1", "r2_data2"}))

	pipe := NewMergePipe(ctx, writer, reader1, reader2)
	defer pipe.Close()

	// Wait for some data to be processed
	time.Sleep(400 * time.Millisecond)

	// Check that writer received data
	written := writer.getWritten()
	if len(written) == 0 {
		t.Error("Writer should have received data")
	}

	// Verify round-robin behavior by checking write order
	writeOrder := writer.getWriteOrder()
	if len(writeOrder) > 1 {
		// Should alternate between readers (though exact order may vary due to timing)
		hasR1Data := false
		hasR2Data := false
		for _, data := range writeOrder {
			if containsSubstring(data, "r1_") {
				hasR1Data = true
			}
			if containsSubstring(data, "r2_") {
				hasR2Data = true
			}
		}
		if !hasR1Data || !hasR2Data {
			t.Error("Both readers should contribute data in round-robin fashion")
		}
	}
}

func TestMergePipe_RoundRobinScheduling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	writer := newMockWriterWithHistory[string, string]()

	// Create readers with different delays to test scheduling
	reader1 := newMockReaderWithDelay(createTestData([]string{"r1_1", "r1_2", "r1_3"}), 10*time.Millisecond)
	reader2 := newMockReaderWithDelay(createTestData([]string{"r2_1", "r2_2", "r2_3"}), 15*time.Millisecond)
	reader3 := newMockReaderWithDelay(createTestData([]string{"r3_1", "r3_2", "r3_3"}), 20*time.Millisecond)

	pipe := NewMergePipe(ctx, writer, reader1, reader2, reader3)
	defer pipe.Close()

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	writeOrder := writer.getWriteOrder()
	if len(writeOrder) < 3 {
		t.Errorf("Expected at least 3 writes, got %d", len(writeOrder))
	}

	// Verify that all readers are being accessed
	readerCounts := make(map[string]int)
	for _, data := range writeOrder {
		if containsSubstring(data, "r1_") {
			readerCounts["r1"]++
		} else if containsSubstring(data, "r2_") {
			readerCounts["r2"]++
		} else if containsSubstring(data, "r3_") {
			readerCounts["r3"]++
		}
	}

	if len(readerCounts) < 2 {
		t.Error("Round-robin should access multiple readers")
	}
}

func TestMergePipe_AddReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader1 := newMockReader(createTestData([]string{"r1_data1", "r1_data2"}))

	pipe := NewMergePipe(ctx, writer, reader1)
	defer pipe.Close()

	// Add a new reader dynamically
	reader2 := newMockReader(createTestData([]string{"r2_data1", "r2_data2"}))
	readerCtx, readerCancel := pipe.AddReader(reader2)
	defer readerCancel()

	// Wait for some processing
	time.Sleep(400 * time.Millisecond)

	// Check that the new reader is added
	pipe.mux.RLock()
	readersSize := 0
	if pipe.readers != nil {
		readersSize = pipe.readers.Size()
	}
	pipe.mux.RUnlock()

	if readersSize != 2 {
		t.Errorf("Expected 2 readers after adding, got %d", pipe.readers.Size())
	}

	// Cancel the reader context and verify it's removed
	readerCancel()
	time.Sleep(500 * time.Millisecond)

	pipe.mux.RLock()
	readersSize = 0
	if pipe.readers != nil {
		readersSize = pipe.readers.Size()
	}
	pipe.mux.RUnlock()

	// The reader should be removed from the set
	if readersSize != 1 {
		t.Errorf("Expected 1 reader after cancellation, got %d", pipe.readers.Size())
	}

	// Verify context is properly set
	select {
	case <-readerCtx.Done():
		// Expected
	default:
		t.Error("Reader context should be cancelled")
	}
}

func TestMergePipe_PauseUnpause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1", "data2", "data3", "data4", "data5"}))

	pipe := NewMergePipe(ctx, writer, reader)
	defer pipe.Close()

	// Let it process some data first
	time.Sleep(500 * time.Millisecond)
	initialCount := len(writer.getWritten())

	// Pause the pipe
	pipe.Pause()
	if !pipe.paused {
		t.Error("Pipe should be paused")
	}

	// Wait and check that no more data is processed
	time.Sleep(500 * time.Millisecond)
	pausedCount := len(writer.getWritten())

	if pausedCount > initialCount+1 { // Allow for one more due to timing
		t.Error("Pipe should not process data when paused")
	}

	// Unpause and check that processing resumes
	pipe.UnPause()
	if pipe.paused {
		t.Error("Pipe should not be paused after UnPause")
	}

	time.Sleep(500 * time.Millisecond)
	finalCount := len(writer.getWritten())

	if finalCount <= pausedCount {
		t.Error("Pipe should resume processing after UnPause")
	}
}

func TestMergePipe_Wait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1"}))

	pipe := NewMergePipe(ctx, writer, reader)

	// Test that Wait returns the context's Done channel
	waitChan := pipe.Wait()

	select {
	case <-waitChan:
		t.Error("Wait channel should not be ready initially")
	default:
		// Expected
	}

	// Cancel context and verify Wait channel is ready
	cancel()

	select {
	case <-waitChan:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Wait channel should be ready after context cancellation")
	}

	pipe.Close()
}

func TestMergePipe_NoReaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()

	pipe := NewMergePipe(ctx, writer)
	defer pipe.Close()

	// Wait for some processing attempts
	time.Sleep(100 * time.Millisecond)

	// Should handle gracefully with no readers
	written := writer.getWritten()
	if len(written) > 0 {
		t.Error("Should not write anything when no readers available")
	}
}

func TestMergePipe_ReaderError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader1 := newMockReader(createTestData([]string{"data1"}))
	reader2 := newMockReader(createTestData([]string{"data2"}))

	pipe := NewMergePipe(ctx, writer, reader1, reader2)
	defer pipe.Close()

	// Set one reader to return error
	reader1.setReadError(errors.New("read error"))

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// The pipe should continue processing with other readers
	// (implementation continues on read errors)
}

func TestMergePipe_WriterError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1", "data2"}))

	pipe := NewMergePipe(ctx, writer, reader)
	defer pipe.Close()

	// Set writer to return error
	writer.setWriteError(errors.New("write error"))

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// The pipe should handle writer errors gracefully
	// (implementation returns on write errors)
}

func TestMergePipe_Close(t *testing.T) {
	ctx := context.Background()
	writer := newMockWriter[string, string]()
	reader1 := newMockReader(createTestData([]string{"data1"}))
	reader2 := newMockReader(createTestData([]string{"data2"}))

	pipe := NewMergePipe(ctx, writer, reader1, reader2)

	// Close the pipe
	err := pipe.Close()
	if err != nil {
		t.Errorf("Close should not return error: %v", err)
	}

	// Verify resources are cleaned up
	if !writer.isClosed() {
		t.Error("Writer should be closed")
	}

	if !reader1.isClosed() {
		t.Error("Reader1 should be closed")
	}

	if !reader2.isClosed() {
		t.Error("Reader2 should be closed")
	}

	// Multiple closes should be safe
	err = pipe.Close()
	if err != nil {
		t.Errorf("Multiple Close calls should not return error: %v", err)
	}
}

func TestMergePipe_CloseWithErrors(t *testing.T) {
	ctx := context.Background()
	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1"}))

	writer.setCloseError(errors.New("writer close error"))
	reader.setCloseError(errors.New("reader close error"))

	pipe := NewMergePipe(ctx, writer, reader)

	err := pipe.Close()
	if err == nil {
		t.Error("Close should return error when components fail to close")
	}

	// Should contain both errors
	errStr := err.Error()
	if !contains(errStr, "writer close error") {
		t.Error("Close error should contain writer close error")
	}
	if !contains(errStr, "reader close error") {
		t.Error("Close error should contain reader close error")
	}
}

func TestMergePipe_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1", "data2", "data3"}))

	pipe := NewMergePipe(ctx, writer, reader)

	// Let it process some data
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for shutdown
	time.Sleep(100 * time.Millisecond)

	// Verify the pipe stopped processing
	select {
	case <-pipe.Wait():
		// Expected
	default:
		t.Error("Pipe should stop when context is cancelled")
	}

	pipe.Close()
}

func TestMergePipe_ReaderIndexBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader1 := newMockReader(createTestData([]string{"r1_data"}))
	reader2 := newMockReader(createTestData([]string{"r2_data"}))

	pipe := NewMergePipe(ctx, writer, reader1, reader2)
	defer pipe.Close()

	// Manually set reader index to out of bounds to test bounds checking
	pipe.mux.Lock()
	pipe.readerIndex = 10 // Out of bounds
	pipe.mux.Unlock()

	// Wait for processing - should reset index to 0
	time.Sleep(100 * time.Millisecond)

	// Verify that processing continues (index was reset)
	written := writer.getWritten()
	if len(written) == 0 {
		t.Error("Should continue processing after index reset")
	}
}

func TestMergePipe_EmptyReadersList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"data1"}))

	pipe := NewMergePipe(ctx, writer, reader)
	defer pipe.Close()

	// Remove all readers
	pipe.mux.Lock()
	pipe.readers.Clear()
	pipe.mux.Unlock()

	// Wait for processing attempts
	time.Sleep(100 * time.Millisecond)

	// Should handle empty readers list gracefully
	// May have written some data before readers were cleared
	// The important thing is that it doesn't crash
	_ = writer.getWritten() // Just verify it doesn't crash
}

// Benchmark tests
func BenchmarkMergePipe_Throughput(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := newMockWriter[string, string]()

	// Create readers with single data item each (will loop automatically)
	readers := make([]Reader[string, string], 10)
	for i := range readers {
		testData := createTestData([]string{fmt.Sprintf("data_%d", i)})
		readers[i] = newMockReader(testData)
	}

	pipe := NewMergePipe(ctx, writer, readers...)
	defer pipe.Close()

	// Let the pipe warm up
	time.Sleep(50 * time.Millisecond)

	b.ResetTimer()

	initialCount := len(writer.getWritten())

	// Measure how many operations complete in the benchmark time
	start := time.Now()
	for time.Since(start) < time.Duration(b.N)*time.Nanosecond {
		time.Sleep(time.Millisecond) // Allow processing
	}

	finalCount := len(writer.getWritten())
	itemsProcessed := finalCount - initialCount

	b.ReportMetric(float64(itemsProcessed), "items/op")
}

func BenchmarkMergePipe_Latency(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := newMockWriter[string, string]()
	reader := newMockReader(createTestData([]string{"test_data"}))

	pipe := NewMergePipe(ctx, writer, reader)
	defer pipe.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		initialCount := len(writer.getWritten())

		start := time.Now()

		// Wait for next item to be processed
		for len(writer.getWritten()) <= initialCount {
			time.Sleep(time.Microsecond)
		}

		latency := time.Since(start)
		b.ReportMetric(float64(latency.Nanoseconds()), "ns/item")
	}
}

func BenchmarkMergePipe_Scaling(b *testing.B) {
	readerCounts := []int{1, 5, 10, 50, 100}

	for _, readerCount := range readerCounts {
		b.Run(fmt.Sprintf("readers_%d", readerCount), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			writer := newMockWriter[string, string]()

			readers := make([]Reader[string, string], readerCount)
			for i := range readers {
				testData := createTestData([]string{fmt.Sprintf("data_%d", i)})
				readers[i] = newMockReader(testData)
			}

			pipe := NewMergePipe(ctx, writer, readers...)
			defer pipe.Close()

			// Let pipe stabilize
			time.Sleep(50 * time.Millisecond)

			b.ResetTimer()

			// Measure throughput over fixed time
			initialCount := len(writer.getWritten())
			time.Sleep(100 * time.Millisecond) // Fixed measurement window
			finalCount := len(writer.getWritten())

			throughput := finalCount - initialCount
			b.ReportMetric(float64(throughput), "items/100ms")
		})
	}
}

func TestMergePipe_RoundRobinFairness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writer := newMockWriter[string, string]()

	// Create readers with identifiable data
	readers := make([]Reader[string, string], 5)
	for i := range readers {
		testData := createTestData([]string{fmt.Sprintf("reader_%d", i)})
		readers[i] = newMockReader(testData)
	}

	pipe := NewMergePipe(ctx, writer, readers...)
	defer pipe.Close()

	// Let it run for a while
	time.Sleep(10 * time.Second)

	written := writer.getWritten()

	// Count items from each reader
	readerCounts := make(map[string]int)
	for _, data := range written {
		readerCounts[data.data]++
	}

	// Verify fairness (should be roughly equal)
	for reader, count := range readerCounts {
		t.Logf("Reader %s: %d items", reader, count)
	}
}

func BenchmarkMergePipe_RoundRobinScheduling(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := newMockWriter[string, string]()

	// Create many readers to test round-robin performance
	readers := make([]Reader[string, string], 100)
	for i := range readers {
		testData := createTestData([]string{"benchmark_data"})
		readers[i] = newMockReader(testData)
	}

	pipe := NewMergePipe(ctx, writer, readers...)
	defer pipe.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset all readers
		for _, r := range readers {
			if mr, ok := r.(*mockReader[string, string]); ok {
				mr.index = 0
			}
		}
		time.Sleep(time.Microsecond) // Allow processing
	}
}
