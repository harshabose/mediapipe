package duplexers

import (
	"context"

	"github.com/harshabose/tools/pkg/buffer"
)

type Buffer[T any] struct {
	buffer buffer.Buffer[T]
	ctx    context.Context
}

func NewBuffer[T any](ctx context.Context, bufsize int) *Buffer[T] {
	return &Buffer[T]{
		ctx:    ctx,
		buffer: buffer.CreateChannelBuffer[T](ctx, bufsize, nil),
	}
}

func (b *Buffer[T]) Generate() (T, error) {
	return b.buffer.Pop(b.ctx)
}

func (b *Buffer[T]) Consume(element T) error {
	return b.buffer.Push(b.ctx, element)
}
