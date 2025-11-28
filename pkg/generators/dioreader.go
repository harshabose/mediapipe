package generators

import (
	"errors"
	"io"

	"github.com/harshabose/mediapipe"
)

type DIOReader struct {
	r    io.Reader // The underlying io.Reader being adapted
	size uint16    // Maximum buffer size for read operations
}

func NewDIODataChannel(dataChannel mediapipe.CanDetach, size uint16) (*DIOReader, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024 // 1KB

	if size < minSize {
		size = minSize
	}

	return &DIOReader{
		r:    rw,
		size: size,
	}, nil
}

func NewDIOReader(reader io.Reader, size uint16) *DIOReader {
	const minSize = 1024 // 1KB

	if size < minSize {
		size = minSize
	}

	return &DIOReader{
		r:    reader,
		size: size,
	}
}

func (r *DIOReader) Generate() ([]byte, error) {
	buffer := make([]byte, r.size)

	for {
		n, err := r.r.Read(buffer)
		if err == nil {
			out := make([]byte, n)
			copy(out, buffer[:n])
			return out, nil
		}

		if errors.Is(err, io.ErrShortBuffer) {
			size := len(buffer) * 2
			buffer = make([]byte, size)

			continue
		}

		return nil, err
	}
}

func (r *DIOReader) Close() error {
	if closer, ok := r.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
