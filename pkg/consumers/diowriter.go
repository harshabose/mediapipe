package consumers

import (
	"fmt"
	"io"

	"github.com/harshabose/mediapipe"
)

type DIOWriter struct {
	w    io.Writer // The underlying io.Writer being adapted
	size uint16    // Maximum buffer size for write operations
}

func NewDIODataChannel(dataChannel mediapipe.CanDetach, size uint16) (*DIOWriter, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024 // 1KB

	if size < minSize {
		size = minSize
	}

	return &DIOWriter{
		w:    rw,
		size: size,
	}, nil
}

func NewDIOWriter(writer io.Writer, size uint16) *DIOWriter {
	const minSize = 1024 // 1KB

	if size < minSize {
		size = minSize
	}

	return &DIOWriter{
		w:    writer,
		size: size,
	}
}

func (w *DIOWriter) Consume(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	if uint16(len(data)) > w.size {
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
