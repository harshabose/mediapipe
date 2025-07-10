package pkg

import (
	"fmt"

	mediapipe "github.com/harshabose/mediapipe"
)

type MockReaderWriter struct{}

func (m *MockReaderWriter) Write(data *mediapipe.Data[string, string]) error {
	value, _ := data.Get()
	fmt.Printf("wrote data: %s", value)

	return nil
}

func (m *MockReaderWriter) Read() (*mediapipe.Data[string, string], error) {
	return mediapipe.Wrap[string, string]("hello world!", nil), nil
}

func (m *MockReaderWriter) Close() error {
	return nil
}
