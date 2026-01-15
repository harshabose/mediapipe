package mediapipe

import (
	"reflect"

	"github.com/pion/datachannel"
)

type CanDetach interface {
	Detach() (datachannel.ReadWriteCloser, error)
}

func isZeroSafe[T any](v T) bool {
	// This catches:
	// - true nil interfaces
	// - interfaces holding nil pointers
	if any(v) == nil {
		return true
	}

	// This catches:
	// - zero value structs
	// - zero value primitives
	// - nil slices
	// - empty strings
	return reflect.ValueOf(v).IsZero()
}
