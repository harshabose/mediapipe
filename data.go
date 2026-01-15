package mediapipe

// Data wraps a value of type T along with a transformation function that can
// produce a derived value of type D on demand. It is the unit that flows
// through Readers and Writers in this package: Readers emit `*Data[D, T]`
// instances and Writers accept them. The transformer is applied by callers
// via `Get` so expensive conversions can be deferred and performed only when
// needed.
type Data[D, T any] struct {
    data        T                  // The source data to be transformed
    transformer func(T) (D, error) // Function that transforms T into D
}

// Wrap constructs a `Data` wrapper from a value and a non‑nil transformer. The
// returned `*Data` can later yield the transformed value through `Get`.
func Wrap[D, T any](data T, transformer func(T) (D, error)) *Data[D, T] {
    if transformer == nil {
        panic("transformer cannot be nil - this should not happen")
    }
    return &Data[D, T]{
        data:        data,
        transformer: transformer,
    }
}

// Get applies the stored transformer to the underlying source value and
// returns the derived value. Any error from the transformer is propagated.
func (d *Data[D, T]) Get() (D, error) {
    return d.transformer(d.data)
}
