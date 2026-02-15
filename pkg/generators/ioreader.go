package generators

import (
	"context"
	"errors"
	"io"
)

type ChuckToStreamReader struct {
	r   io.Reader
	buf []byte
}

func NewChuckToStreamReader(r io.Reader) io.Reader {
	return &ChuckToStreamReader{r: r, buf: make([]byte, 0)}
}

func (r *ChuckToStreamReader) Read(p []byte) (n int, err error) {
	if len(r.buf) > 0 {
		n = copy(p, r.buf)
		r.buf = r.buf[n:]

		return n, nil
	}

	buf := make([]byte, len(p))

	for {
		n, err = r.r.Read(buf)
		if errors.Is(err, io.ErrShortBuffer) {
			buf = make([]byte, len(buf)*2)
			continue
		}
		if err != nil {
			return n, err
		}

		d := copy(p, buf[:n])
		if d < n {
			r.buf = buf[d:n]
		}

		return d, nil
	}
}

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

	// 3. Handle EOF
	if errors.Is(err, io.EOF) {
		return nil, nil
	}

	return nil, err

}
