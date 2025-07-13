package consumers

import (
	"fmt"
	"io"
	"math"

	"github.com/pion/webrtc/v4"
)

type DIOWriter struct {
	w    io.Writer // The underlying io.Writer being adapted
	size uint32    // Maximum buffer size for write operations
}

func NewDataChannel(dataChannel *webrtc.DataChannel, size uint32) (*DIOWriter, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // default Pion max value

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &DIOWriter{
		w:    rw,
		size: size,
	}, nil
}

func NewWriter(writer io.Writer, size uint32) (*DIOWriter, error) {
	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // reasonable max value

	if size < minSize || size > maxSize {
		return nil, fmt.Errorf("buffer size %d out of range [%d, %d]", size, minSize, maxSize)
	}

	return &DIOWriter{
		w:    writer,
		size: size,
	}, nil
}

func (w *DIOWriter) Consume(data []byte) error {
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

func (w *DIOWriter) Close() error {
	if closer, ok := w.w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
