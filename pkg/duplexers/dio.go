package duplexers

import (
	"fmt"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"

	"github.com/harshabose/mediapipe/pkg/consumers"
	"github.com/harshabose/mediapipe/pkg/generators"
)

type DIO struct {
	*consumers.DIOWriter
	*generators.DIOReader
}

func NewDIO(dc *webrtc.DataChannel, size uint16) (*DIO, error) {
	var rw datachannel.ReadWriteCloser
	var err error

	for {
		rw, err = dc.Detach()
		if err != nil {
			fmt.Println("data channel not open yet. waiting for 100ms to retry...")
			time.Sleep(100 * time.Millisecond)
			continue
		}
		break
	}
	const minsize = 1024

	if size < minsize {
		size = minsize
	}

	return &DIO{
		DIOWriter: consumers.NewDIOWriter(rw, size),
		DIOReader: generators.NewDIOReader(rw, size),
	}, nil
}

func (rw *DIO) Close() error {
	if err := rw.DIOWriter.Close(); err != nil {
		return err
	}

	if err := rw.DIOReader.Close(); err != nil {
		return err
	}

	return nil
}
