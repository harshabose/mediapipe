package duplexers

import (
	"context"

	"github.com/harshabose/tools/pkg/buffer"
)

type Buffer[T any] struct {
	buffer.Buffer[T]
}

func NewBuffer[T any](ctx context.Context, bufsize int) *Buffer[T] {
	return &Buffer[T]{
		Buffer: buffer.NewChannelBuffer[T](ctx, uint(bufsize), 1),
	}
}

func NewBufferGeneratorConsumer[T any](b buffer.Buffer[T]) *Buffer[T] {
	return &Buffer[T]{
		Buffer: b,
	}
}

func (b *Buffer[T]) Generate(ctx context.Context) (T, error) {
	return b.Pop(ctx)
}

func (b *Buffer[T]) Consume(ctx context.Context, element T) error {
	return b.Push(ctx, element)
}
