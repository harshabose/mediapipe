package mediapipe

type Data[D, T any] struct {
	data        T                  // The source data to be transformed
	transformer func(T) (D, error) // Function that transforms T into D
}

func Wrap[D, T any](data T, transformer func(T) (D, error)) *Data[D, T] {
	if transformer == nil {
		panic("transformer cannot be nil - this should not happen")
	}
	return &Data[D, T]{
		data:        data,
		transformer: transformer,
	}
}

func (d *Data[D, T]) Get() (D, error) {
	return d.transformer(d.data)
}
