package generators

import (
	"errors"
	"io"

	"github.com/pion/webrtc/v4"
)

type DIOReader struct {
	r    io.Reader // The underlying io.Reader being adapted
	size uint16    // Maximum buffer size for read operations
}

func NewDIODataChannel(dataChannel *webrtc.DataChannel, size uint16) (*DIOReader, error) {
	rw, err := dataChannel.Detach()
	if err != nil {
		return nil, err
	}

	const minSize = 1024 // 1KB
	// const maxSize = math.MaxUint16 // default Pion max value

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
	// const maxSize = math.MaxUint16 // default Pion max value

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
	n, err := r.r.Read(buffer)
	if err == nil {
		return buffer[:n], nil
	}

	// Handle short buffer by retrying with larger buffer
	if errors.Is(err, io.ErrShortBuffer) {
		buffer = make([]byte, r.size*2) // Try with double the size
		n, err = r.r.Read(buffer)
		if err != nil {
			return nil, err
		}
		return buffer[:n], nil
	}

	return nil, err
}

func (r *DIOReader) Close() error {
	if closer, ok := r.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
