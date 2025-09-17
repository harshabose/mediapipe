package mediapipe

import "github.com/pion/datachannel"

type CanDetach interface {
	Detach() (datachannel.ReadWriteCloser, error)
}
