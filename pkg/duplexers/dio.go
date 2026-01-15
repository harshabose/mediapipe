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
	*consumers.IOWriter
	*generators.IOReader
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

	return &DIO{
		IOWriter: consumers.NewIOWriter(rw, size),
		IOReader: generators.NewIOReader(rw, size),
	}, nil
}
