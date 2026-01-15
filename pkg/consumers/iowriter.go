package consumers

import (
	"context"
	"fmt"
	"io"
	"math"
)

type IOWriter struct {
	w    io.Writer // The underlying io.Writer being adapted
	size uint16    // Maximum buffer size for write operations
}

func NewIOWriter(writer io.Writer, size uint16) *IOWriter {
	const minSize = 1024           // 1KB
	const maxSize = math.MaxUint16 // reasonable max value

	if size < minSize || size > maxSize {
		size = minSize
	}

	return &IOWriter{
		w:    writer,
		size: size,
	}
}

func (w *IOWriter) Consume(_ context.Context, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	if uint16(len(data)) > w.size {
		return fmt.Errorf("data size %d exceeds max buffer size %d", len(data), w.size)
	}

	n, err := w.w.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write (IOWriter). err=%w", err)
	}

	if n != len(data) {
		return fmt.Errorf("short write: wrote %d of %d bytes", n, len(data))
	}

	return nil
}
