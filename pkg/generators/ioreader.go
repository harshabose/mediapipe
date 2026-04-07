package generators

import (
	"context"
	"errors"
	"io"
)

type IOReader struct {
	r    io.Reader // The underlying io.Reader being adapted
	size uint16    // Maximum buffer size for read operations
}

func NewIOReader(reader io.Reader, size uint16) *IOReader {
	const minSize = 1024 // 1KB
	// const maxSize = math.MaxUint16 // default Pion max value

	if size < minSize {
		size = minSize
	}

	return &IOReader{
		r:    reader,
		size: size,
	}
}

func (r *IOReader) Generate(_ context.Context) ([]byte, error) {
	buf := make([]byte, r.size)

	n, err := r.r.Read(buf)
	if err == nil {
		return buf[:n], nil
	}

	if errors.Is(err, io.ErrShortBuffer) {
		bigBuf := make([]byte, int(r.size)*2)
		n, err = r.r.Read(bigBuf)
		if err != nil {
			return nil, err
		}
		return bigBuf[:n], nil
	}

	if errors.Is(err, io.EOF) {
		return nil, nil
	}

	return nil, err
}
