package generators

import (
	"errors"
	"io"

	"github.com/pion/webrtc/v4"
)

type IOReader struct {
	r    io.Reader // The underlying io.Reader being adapted
	size uint16    // Maximum buffer size for read operations
}

func NewIODataChannel(dataChannel *webrtc.DataChannel, size uint16) (*IOReader, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024 // 1KB
	// const maxSize = math.MaxUint16 // default Pion max value

	if size < minSize {
		size = minSize
	}

	return &IOReader{
		r:    rw,
		size: size,
	}, nil
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

func (r *IOReader) Generate() ([]byte, error) {
	// return io.ReadAll(io.LimitReader(r.r, int64(r.size)))
	buf := make([]byte, r.size)
	n, err := r.r.Read(buf)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	return buf[:n], nil
}

func (r *IOReader) Close() error {
	if closer, ok := r.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
