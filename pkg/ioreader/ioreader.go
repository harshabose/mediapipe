package ioreader

import (
	"fmt"
	"io"
	"math"

	"github.com/pion/webrtc/v4"
)

// Reader provides a universal adapter that converts any io.Reader into a
// CanGenerate[[]byte], enabling seamless integration with the universal media
// routing system as a data source.
//
// This adapter allows any io.Reader implementation to participate in the media
// pipeline as a data source:
//   - TCP/UDP network connections (reading incoming data)
//   - Serial port connections (reading sensor data)
//   - File handles (reading file content)
//   - HTTP response bodies (reading downloaded content)
//   - Pipe connections (reading from other processes)
//   - WebSocket connections (reading incoming messages)
//   - Stdin (reading user input)
//   - Any custom io.Reader implementation
//
// The Reader maintains a configurable buffer size limit to prevent memory
// exhaustion when reading from potentially unlimited data sources. This is
// particularly important for network connections where the remote end might
// send arbitrarily large amounts of data.
//
// Type safety is maintained through the []byte interface, making this adapter
// suitable for binary data streaming scenarios common in media routing applications.
type Reader struct {
	r    io.Reader // The underlying io.Reader being adapted
	size uint32    // Maximum buffer size for read operations
}

// NewDataChannel creates a Reader from a detached WebRTC DataChannel for reading incoming data.
//
// This factory function specifically handles WebRTC DataChannel integration by
// detaching the DataChannel and wrapping it as an io.Reader. The detachment
// is necessary because Pion WebRTC does not support mixing the message-based
// DataChannel API (.Send(), .OnMessage) with the io-based API (.Read(), .Write())
// simultaneously.
//
// The size parameter should match the value used in SettingEngine.SetSCTPMaxReceiveBufferSize()
// to ensure consistent buffer management throughout the WebRTC pipeline. This alignment
// prevents situations where the Reader might attempt to read more data than the
// underlying SCTP transport can provide.
//
// Prerequisites:
//   - SettingEngine.DetachDataChannels() must be called before creating the peer connection
//   - SettingEngine.SetSCTPMaxReceiveBufferSize(size) should be called with the same size value
//   - The DataChannel must be in the "open" state before detachment
//
// Parameters:
//   - dataChannel: A Pion WebRTC DataChannel in the open state
//   - size: Maximum receive buffer size (must match SCTP configuration)
//
// Buffer size constraints:
//   - Minimum: 1KB (1024 bytes) - ensures reasonable chunk sizes for network efficiency
//   - Maximum: 65535 bytes (math.MaxUint16) - matches Pion's default SCTP message size limit
//
// Returns a Reader that can be used with the universal media routing system
// for receiving data over the WebRTC DataChannel.
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
//	// Wait for DataChannel to open, then create Reader
//	dc.OnOpen(func() {
//	    reader, err := NewDataChannel(dc, bufferSize)
//	    anyReader := NewIdentityAnyReader(reader)
//	    buffered := NewBufferedReader(ctx, anyReader, 100)
//	})
func NewDataChannel(dataChannel *webrtc.DataChannel, size uint32) (*Reader, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // default Pion max value

	if size == 0 {
		size = maxSize
	}

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &Reader{
		r:    rw,
		size: size,
	}, nil
}

// NewReader creates a Reader from any io.Reader with the specified buffer size limit.
//
// This is the general-purpose constructor for wrapping any io.Reader implementation
// in the universal media routing system. It provides a clean way to adapt existing
// io.Reader sources without the WebRTC-specific setup required by NewDataChannel.
//
// Parameters:
//   - reader: Any io.Reader implementation to be adapted
//   - size: Maximum buffer size for read operations
//
// Buffer size constraints:
//   - Minimum: 1KB (1024 bytes) - ensures reasonable chunk sizes
//   - Maximum: 65535 bytes (math.MaxUint16) - prevents excessive memory usage
//
// Example:
//
//	// Adapt a file for the media routing system
//	file, err := os.Open("data.bin")
//	reader, err := NewReader(file, 8192) // 8KB buffer
//
//	// Adapt a network connection
//	conn, err := net.Dial("tcp", "example.com:8080")
//	reader, err := NewReader(conn, 16384) // 16KB buffer
//
//	// Adapt stdin
//	reader, err := NewReader(os.Stdin, 4096) // 4KB buffer
func NewReader(reader io.Reader, size uint32) (*Reader, error) {
	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // reasonable max value

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &Reader{
		r:    reader,
		size: size,
	}, nil
}

// Generate implements the CanGenerate[[]byte] interface by reading all available data
// from the underlying io.Reader up to the configured buffer size limit.
//
// This method uses io.ReadAll with io.LimitReader to ensure memory safety while
// reading from potentially unlimited data sources. The read operation will continue
// until either:
//   - EOF is reached on the underlying reader
//   - The buffer size limit is reached
//   - An error occurs during reading
//
// The buffer size limit prevents memory exhaustion attacks or unintentional resource
// consumption when connected to data sources that might provide unlimited data streams.
//
// For network connections, this method will block until data is available or the
// connection is closed. For file handles, it will read until EOF or the size limit.
// For interactive sources like stdin, it will read until a newline or the size limit.
//
// Returns:
//   - []byte: All data read from the underlying reader (up to size limit)
//   - error: Any error that occurred during reading, including io.EOF
func (r *Reader) Generate() ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.r, int64(r.size)))
}

// Close closes the underlying reader if it implements io.Closer.
//
// This method provides proper resource cleanup for readers that need explicit
// closing (such as files, network connections, etc.). If the underlying reader
// does not implement io.Closer, this method is a no-op.
//
// Returns:
//   - error: Any error that occurred during closing, or nil
func (r *Reader) Close() error {
	if closer, ok := r.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
