package mediapipe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Mock implementations for testing

// mockReader implements Reader[D, T] interface for testing
type mockReader[D, T any] struct {
	data     []*Data[D, T]
	index    int
	closed   bool
	readErr  error
	closeErr error
	mu       sync.Mutex
}

func newMockReader[D, T any](data []*Data[D, T]) *mockReader[D, T] {
	return &mockReader[D, T]{
		data: data,
	}
}

func (r *mockReader[D, T]) Read() (*Data[D, T], error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, errors.New("reader is closed")
	}

	if r.readErr != nil {
		return nil, r.readErr
	}

	if r.index >= len(r.data) {
		// Reset index to loop through data continuously for testing
		r.index = 0
	}

	if len(r.data) == 0 {
		// Block to simulate waiting for data
		time.Sleep(100 * time.Millisecond)
		return nil, errors.New("no data available")
	}

	data := r.data[r.index]
	r.index++
	time.Sleep(100 * time.Millisecond)
	return data, nil
}

func (r *mockReader[D, T]) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.closeErr
}

func (r *mockReader[D, T]) setReadError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readErr = err
}

func (r *mockReader[D, T]) setCloseError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeErr = err
}

func (r *mockReader[D, T]) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// mockWriter implements Writer[D, T] interface for testing
type mockWriter[D, T any] struct {
	written  []*Data[D, T]
	closed   bool
	writeErr error
	closeErr error
	mu       sync.Mutex
}

func newMockWriter[D, T any]() *mockWriter[D, T] {
	return &mockWriter[D, T]{
		written: make([]*Data[D, T], 0),
	}
}

func (w *mockWriter[D, T]) Write(data *Data[D, T]) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return errors.New("writer is closed")
	}

	if w.writeErr != nil {
		return w.writeErr
	}

	w.written = append(w.written, data)
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (w *mockWriter[D, T]) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

func (w *mockWriter[D, T]) getWritten() []*Data[D, T] {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]*Data[D, T], len(w.written))
	copy(result, w.written)
	return result
}

func (w *mockWriter[D, T]) setWriteError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeErr = err
}

func (w *mockWriter[D, T]) setCloseError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeErr = err
}

func (w *mockWriter[D, T]) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *mockWriter[D, T]) getWrittenCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.written)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

type fastMockWriter[D, T any] struct {
	written []*Data[D, T]
	mu      sync.Mutex
}

func (w *fastMockWriter[D, T]) Write(data *Data[D, T]) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written = append(w.written, data)
	return nil // No sleep
}

func (w *fastMockWriter[D, T]) Close() error { return nil }
func (w *fastMockWriter[D, T]) getWrittenCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.written)
}

// Test helper functions
func createTestData(values []string) []*Data[string, string] {
	var data []*Data[string, string]
	for _, v := range values {
		data = append(data, Wrap(v, func(s string) (string, error) {
			return s, nil
		}))
	}
	return data
}

func TestNewFanoutPipe(t *testing.T) {
	ctx := context.Background()
	reader := newMockReader(createTestData([]string{"test1", "test2"}))
	writer1 := newMockWriter[string, string]()
	writer2 := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer1, writer2)

	if pipe == nil {
		t.Fatal("NewFanoutPipe returned nil")
	}

	if pipe.reader != reader {
		t.Error("Reader not set correctly")
	}

	if pipe.writers.Size() != 2 {
		t.Errorf("Expected 2 writers, got %d", pipe.writers.Size())
	}

	time.Sleep(400 * time.Millisecond)

	if pipe.paused {
		t.Error("Pipe should not be paused initially")
	}

	// Clean up
	err := pipe.Close()
	if err != nil {
		t.Errorf("error closing fanoutpipe: %s", err.Error())
	}
}

func TestFanoutPipe_BasicFunctionality(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	testData := createTestData([]string{"data1", "data2", "data3"})
	reader := newMockReader(testData)
	writer1 := newMockWriter[string, string]()
	writer2 := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer1, writer2)
	defer pipe.Close()

	// Wait for some data to be processed
	time.Sleep(400 * time.Millisecond)

	// Check that both writers received the data
	written1 := writer1.getWritten()
	written2 := writer2.getWritten()

	if len(written1) == 0 {
		t.Error("Writer1 should have received data")
	}

	if len(written2) == 0 {
		t.Error("Writer2 should have received data")
	}

	// Verify data content
	for i, data := range written1 {
		if i < len(testData) {
			expected, _ := testData[i].Get()
			actual, _ := data.Get()
			if actual != expected {
				t.Errorf("Writer1 data mismatch at index %d: expected %s, got %s", i, expected, actual)
			}
		}
	}
}

func TestFanoutPipe_AddWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	testData := createTestData([]string{"data1", "data2", "data3"})
	reader := newMockReader(testData)
	writer1 := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer1)
	defer pipe.Close()

	// Add a new writer dynamically
	writer2 := newMockWriter[string, string]()
	writerCtx, writerCancel := pipe.AddWriter(writer2)
	defer writerCancel()

	// Wait for some processing
	time.Sleep(400 * time.Millisecond)

	// Check that the new writer is added
	pipe.mux.RLock()
	writersSize := 0
	if pipe.writers != nil {
		writersSize = pipe.writers.Size()
	}
	pipe.mux.RUnlock()

	if writersSize != 2 {
		t.Errorf("Expected 2 writers after adding, got %d", writersSize)
	}

	// Cancel the writer context and verify it's removed
	writerCancel()
	time.Sleep(50 * time.Millisecond)

	// The writer should be removed from the set
	pipe.mux.RLock()
	writersSize = 0
	if pipe.writers != nil {
		writersSize = pipe.writers.Size()
	}
	pipe.mux.RUnlock()

	if writersSize != 1 {
		t.Errorf("Expected 1 writer after cancellation, got %d", writersSize)
	}

	// Verify context is properly set
	select {
	case <-writerCtx.Done():
		// Expected
	default:
		t.Error("Writer context should be cancelled")
	}
}

func TestFanoutPipe_PauseUnpause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	testData := createTestData([]string{"data1", "data2", "data3", "data4", "data5"})
	reader := newMockReader(testData)
	writer := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer)
	defer pipe.Close()

	// Let it process some data first
	time.Sleep(400 * time.Millisecond)
	initialCount := len(writer.getWritten())

	// Pause the pipe
	pipe.Pause()
	if !pipe.paused {
		t.Error("Pipe should be paused")
	}

	// Wait and check that no more data is processed
	time.Sleep(400 * time.Millisecond)
	pausedCount := len(writer.getWritten())

	if pausedCount > initialCount+1 { // Allow for one more due to timing
		t.Error("Pipe should not process data when paused")
	}

	// Unpause and check that processing resumes
	pipe.UnPause()
	if pipe.paused {
		t.Error("Pipe should not be paused after UnPause")
	}

	time.Sleep(400 * time.Millisecond)
	finalCount := len(writer.getWritten())

	if finalCount <= pausedCount {
		t.Error("Pipe should resume processing after UnPause")
	}
}

func TestFanoutPipe_Wait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := newMockReader(createTestData([]string{"data1"}))
	writer := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer)

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

func TestFanoutPipe_ReaderError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	reader := newMockReader(createTestData([]string{"data1"}))
	writer := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer)
	defer pipe.Close()

	// Set reader to return error
	reader.setReadError(errors.New("read error"))

	// Wait for the pipe to handle the error
	time.Sleep(100 * time.Millisecond)

	// The pipe should handle the error gracefully
	// (implementation continues on read errors)
}

func TestFanoutPipe_WriterError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	testData := createTestData([]string{"data1", "data2"})
	reader := newMockReader(testData)
	writer1 := newMockWriter[string, string]()
	writer2 := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer1)
	defer pipe.Close()

	pipe.AddWriter(writer2)

	time.Sleep(400 * time.Millisecond)
	// Set one writer to return error
	writer2.setWriteError(errors.New("write error"))

	// Wait for processing
	time.Sleep(400 * time.Millisecond)

	pipe.mux.RLock()
	writers := pipe.writers.Size()
	pipe.mux.RUnlock()

	if writers != 1 {
		t.Errorf("error should have removed the writer: %d", writers)
	}

	// The pipe should continue processing despite writer errors
	// Writer1 should still receive data
	written1 := writer1.getWritten()
	if len(written1) == 0 {
		t.Error("Writer2 should have received data despite Writer1 error")
	}
}

func TestFanoutPipe_Close(t *testing.T) {
	ctx := context.Background()
	reader := newMockReader(createTestData([]string{"data1"}))
	writer1 := newMockWriter[string, string]()
	writer2 := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer1, writer2)

	// Close the pipe
	err := pipe.Close()
	if err != nil {
		t.Errorf("Close should not return error: %v", err)
	}

	// Verify resources are cleaned up
	if !reader.isClosed() {
		t.Error("Reader should be closed")
	}

	if !writer1.isClosed() {
		t.Error("Writer1 should be closed")
	}

	if !writer2.isClosed() {
		t.Error("Writer2 should be closed")
	}

	// Multiple closes should be safe
	err = pipe.Close()
	if err != nil {
		t.Errorf("Multiple Close calls should not return error: %v", err)
	}
}

func TestFanoutPipe_CloseWithErrors(t *testing.T) {
	ctx := context.Background()
	reader := newMockReader(createTestData([]string{"data1"}))
	writer := newMockWriter[string, string]()

	reader.setCloseError(errors.New("reader close error"))
	writer.setCloseError(errors.New("writer close error"))

	pipe := NewFanoutPipe(ctx, reader, writer)

	err := pipe.Close()
	if err == nil {
		t.Error("Close should return error when components fail to close")
		return
	}

	// Should contain both errors
	errStr := err.Error()
	if !contains(errStr, "reader close error") {
		t.Error("Close error should contain reader close error")
	}
	if !contains(errStr, "writer close error") {
		t.Error("Close error should contain writer close error")
	}
}

func TestFanoutPipe_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := newMockReader(createTestData([]string{"data1", "data2", "data3"}))
	writer := newMockWriter[string, string]()

	pipe := NewFanoutPipe(ctx, reader, writer)

	// Let it process some data
	time.Sleep(500 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for shutdown
	time.Sleep(500 * time.Millisecond)

	// Verify the pipe stopped processing
	select {
	case <-pipe.Wait():
		// Expected
	default:
		t.Error("Pipe should stop when context is cancelled")
	}

	pipe.Close()
}

func TestWriterWrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := newMockWriter[string, string]()
	wrap, wrapCtx, wrapCancel := newWriterWrap(ctx, writer)

	testData := Wrap("test", func(s string) (string, error) { return s, nil })

	// Test normal write
	err := wrap.Write(testData)
	if err != nil {
		t.Errorf("Write should not return error: %v", err)
	}

	written := writer.getWritten()
	if len(written) != 1 {
		t.Errorf("Expected 1 written item, got %d", len(written))
	}

	// Test write after context cancellation
	wrapCancel()
	err = wrap.Write(testData)
	if err == nil {
		t.Error("Write should return error after context cancellation")
	}

	// Test close
	err = wrap.Close()
	if err != nil {
		t.Errorf("Close should not return error: %v", err)
	}

	// Verify context is cancelled
	select {
	case <-wrapCtx.Done():
		// Expected
	default:
		t.Error("Wrapper context should be cancelled")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
