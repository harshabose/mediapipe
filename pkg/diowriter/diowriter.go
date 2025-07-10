package diowriter

import (
	"fmt"
	"io"
	"math"

	"github.com/pion/webrtc/v4"
)

// Writer provides a universal adapter that converts any io.Writer into a
// CanConsume[[]byte], enabling seamless integration with the universal media
// routing system as a data destination.
//
// This adapter allows any io.Writer implementation to participate in the media
// pipeline as a data destination:
//   - TCP/UDP network connections (sending outgoing data)
//   - Serial port connections (sending control commands)
//   - File handles (writing to files)
//   - HTTP request bodies (uploading data)
//   - Pipe connections (sending to other processes)
//   - WebSocket connections (sending outgoing messages)
//   - Stdout/Stderr (writing output/logs)
//   - Any custom io.Writer implementation
//
// The Writer maintains a configurable buffer size limit to prevent attempts
// to write oversized data that might overwhelm the destination or indicate
// configuration mismatches in the pipeline.
//
// Type safety is maintained through the []byte interface, making this adapter
// suitable for binary data streaming scenarios common in media routing applications.
type Writer struct {
	w    io.Writer // The underlying io.Writer being adapted
	size uint32    // Maximum buffer size for write operations
}

// NewDataChannel creates a Writer from a detached WebRTC DataChannel for sending outgoing data.
//
// This factory function specifically handles WebRTC DataChannel integration by
// detaching the DataChannel and wrapping it as an io.Writer. The detachment
// is necessary because Pion WebRTC does not support mixing the message-based
// DataChannel API (.Send(), .OnMessage) with the io-based API (.Read(), .Write())
// simultaneously.
//
// The size parameter should match the value used in SettingEngine.SetSCTPMaxReceiveBufferSize()
// to ensure consistent buffer management throughout the WebRTC pipeline. This alignment
// prevents situations where the Writer might attempt to write more data than the
// receiving end can handle.
//
// Prerequisites:
//   - SettingEngine.DetachDataChannels() must be called before creating the peer connection
//   - SettingEngine.SetSCTPMaxReceiveBufferSize(size) should be called with the same size value
//   - The DataChannel must be in the "open" state before detachment
//
// Parameters:
//   - dataChannel: A Pion WebRTC DataChannel in the open state
//   - size: Maximum send buffer size (should match SCTP configuration)
//
// Buffer size constraints:
//   - Minimum: 1KB (1024 bytes) - ensures reasonable chunk sizes for network efficiency
//   - Maximum: 65535 bytes (math.MaxUint16) - matches Pion's default SCTP message size limit
//
// Returns a Writer that can be used with the universal media routing system
// for sending data over the WebRTC DataChannel.
//
// Example:
//
//	// Configure WebRTC with detached DataChannels
//	se := webrtc.SettingEngine{}
//	bufferSize := uint32(32 * 1024) // 32KB
//	se.SetSCTPMaxReceiveBufferSize(bufferSize)
//	se.DetachDataChannels()
//
//	// Create peer connection and DataChannel
//	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{}, &se)
//	dc, err := pc.CreateDataChannel("data", nil)
//
//	// Wait for DataChannel to open, then create Writer
//	dc.OnOpen(func() {
//	    writer, err := NewDataChannel(dc, bufferSize)
//	    anyWriter := NewIdentityAnyWriter(writer)
//	    buffered := NewBufferedWriter(ctx, anyWriter, 100)
//	})
func NewDataChannel(dataChannel *webrtc.DataChannel, size uint32) (*Writer, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // default Pion max value

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &Writer{
		w:    rw,
		size: size,
	}, nil
}

// NewWriter creates a Writer from any io.Writer with the specified buffer size limit.
//
// This is the general-purpose constructor for wrapping any io.Writer implementation
// in the universal media routing system. It provides a clean way to adapt existing
// io.Writer destinations without the WebRTC-specific setup required by NewDataChannel.
//
// Parameters:
//   - writer: Any io.Writer implementation to be adapted
//   - size: Maximum buffer size for write operations
//
// Buffer size constraints:
//   - Minimum: 1KB (1024 bytes) - ensures reasonable chunk sizes
//   - Maximum: 65535 bytes (math.MaxUint16) - prevents excessive memory usage
//
// Example:
//
//	// Adapt a file for the media routing system
//	file, err := os.Create("output.bin")
//	writer, err := NewWriter(file, 8192) // 8KB buffer
//
//	// Adapt a network connection
//	conn, err := net.Dial("tcp", "example.com:8080")
//	writer, err := NewWriter(conn, 16384) // 16KB buffer
//
//	// Adapt stdout
//	writer, err := NewWriter(os.Stdout, 4096) // 4KB buffer
func NewWriter(writer io.Writer, size uint32) (*Writer, error) {
	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // reasonable max value

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &Writer{
		w:    writer,
		size: size,
	}, nil
}

// Consume implements the CanConsume[[]byte] interface by writing data as a single
// message to the underlying io.Writer with proper error handling.
//
// This method ensures that all provided data is written to the underlying writer
// as one complete message, which is particularly important for message-based
// protocols like WebRTC DataChannels where each Write() operation should send
// one discrete message.
//
// For DataChannels, this method will:
//   - Write the entire data as one complete message
//   - Maintain message boundaries for the receiving end
//   - Handle partial writes by ensuring all data is transmitted
//
// For stream-based writers (files, network streams), this method will:
//   - Write data in a loop until all bytes are written
//   - Handle partial writes by tracking progress and retrying
//   - Ensure data integrity across potentially interrupted write operations
//
// The method includes a size check to prevent attempting to write data larger
// than the configured buffer size, which could indicate a configuration mismatch
// or an attempt to send oversized data that the receiving end cannot handle.
//
// Parameters:
//   - data: The byte slice to write to the underlying writer
//
// The write process:
//  1. Validates data size against the configured limit
//  2. Writes data in a loop until all bytes are written
//  3. Handles partial writes by tracking progress and retrying
//  4. Returns descriptive errors for debugging write failures
//
// Returns:
//   - error: Any error that occurred during writing, or nil on success
func (w *Writer) Consume(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	if uint32(len(data)) > w.size {
		return fmt.Errorf("data size %d exceeds max buffer size %d", len(data), w.size)
	}

	written := 0
	for written < len(data) {
		n, err := w.w.Write(data[written:])
		if err != nil {
			return fmt.Errorf("failed to write data: %w", err)
		}
		written += n
	}

	return nil
}

// Close closes the underlying writer if it implements io.Closer.
//
// This method provides proper resource cleanup for writers that need explicit
// closing (such as files, network connections, etc.). If the underlying writer
// does not implement io.Closer, this method is a no-op.
//
// Returns:
//   - error: Any error that occurred during closing, or nil
func (w *Writer) Close() error {
	if closer, ok := w.w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
