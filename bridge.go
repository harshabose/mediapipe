package mediapipe

import (
	"context"
	"sync"

	"github.com/harshabose/tools/pkg/cond"
)

type Bridge[T any] struct {
	data    T
	emitted bool

	cond *cond.ContextCond
	mux  sync.Mutex
}

func NewBridge[T any]() *Bridge[T] {
	b := &Bridge[T]{
		emitted: false,
	}
	b.cond = cond.NewContextCond(&b.mux)

	return b
}

func (b *Bridge[T]) Consume(ctx context.Context, data T) error {
	b.mux.Lock()
	defer b.mux.Unlock()

	for b.emitted == true {
		if err := b.cond.Wait(ctx); err != nil {
			return err
		}
	}

	b.data = data
	b.emitted = true

	b.cond.Signal()

	return nil
}

func (b *Bridge[T]) Wait(ctx context.Context) error {
	b.mux.Lock()
	defer b.mux.Unlock()

	for b.emitted == true {
		if err := b.cond.Wait(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (b *Bridge[T]) Generate(ctx context.Context) (T, error) {
	b.mux.Lock()
	defer b.mux.Unlock()

	var zero T

	for b.emitted == false {
		if err := b.cond.Wait(ctx); err != nil {
			return zero, err
		}
	}

	data := b.data
	b.data = zero
	b.emitted = false

	b.cond.Signal()

	return data, nil
}

func (b *Bridge[T]) Close() {
	b.mux.Lock()
	defer b.mux.Unlock()

	b.cond.Broadcast()
}
