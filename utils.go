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

type Doer interface {
	Done() <-chan struct{}
	Err() error
}

func Do(doers ...Doer) Doer {
	cases := make([]reflect.SelectCase, len(doers))
	for i, doer := range doers {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(doer.Done()),
		}
	}

	chosen, _, _ := reflect.Select(cases)
	return doers[chosen]
}

type Doers []Doer

func NewDoers[T Doer](doers ...T) Doers {
	out := make(Doers, len(doers))
	for i, v := range doers {
		out[i] = v
	}

	return out
}

func (d Doers) Add(extras ...Doer) Doers {
	return append(d, extras...)
}
