package mediapipe

import "context"

type Transformer[D, T any] struct {
	*GenerateTransformer[D, T]
	*ConsumerTransformer[D, T]
}

func NewTransformer[D, T any](g *GenerateTransformer[D, T], c *ConsumerTransformer[D, T]) *Transformer[D, T] {
	return &Transformer[D, T]{
		GenerateTransformer: g,
		ConsumerTransformer: c,
	}
}

type GenerateTransformer[D, T any] struct {
	g CanGenerate[T]

	transformer func(T) (D, error)
}

func NewGenerateTransformer[D, T any](generator CanGenerate[T], transformer func(T) (D, error)) *GenerateTransformer[D, T] {
	return &GenerateTransformer[D, T]{
		g: generator,

		transformer: transformer,
	}
}

func (t *GenerateTransformer[D, T]) Generate(ctx context.Context) (D, error) {
	var zero D

	d, err := t.g.Generate(ctx)
	if err != nil {
		return zero, err
	}

	return t.transformer(d)
}

type ConsumerTransformer[D, T any] struct {
	c           CanConsume[T]
	transformer func(D) (T, error)
}

func NewConsumerTransformer[D, T any](consumer CanConsume[T], transformer func(D) (T, error)) *ConsumerTransformer[D, T] {
	return &ConsumerTransformer[D, T]{
		c:           consumer,
		transformer: transformer,
	}
}

func (t *ConsumerTransformer[D, T]) Consume(ctx context.Context, data D) error {
	d, err := t.transformer(data)
	if err != nil {
		return err
	}

	return t.c.Consume(ctx, d)
}
