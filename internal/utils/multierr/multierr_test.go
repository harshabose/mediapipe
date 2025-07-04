package multierr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCombine(t *testing.T) {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")
	err3 := errors.New("error 3")

	tests := []struct {
		name     string
		errs     []error
		wantNil  bool
		wantText string
	}{
		{
			name:    "nil errors",
			errs:    []error{nil, nil, nil},
			wantNil: true,
		},
		{
			name:     "single error",
			errs:     []error{nil, err1, nil},
			wantNil:  false,
			wantText: "error 1",
		},
		{
			name:     "multiple errors",
			errs:     []error{err1, err2, err3},
			wantNil:  false,
			wantText: "3 errors occurred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Combine(tt.errs...)

			if (err == nil) != tt.wantNil {
				t.Errorf("Combine() nil = %v, want %v", err == nil, tt.wantNil)
				return
			}

			if err != nil && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("Combine() error = %v, want to contain %v", err, tt.wantText)
			}
		})
	}
}

func TestAppend(t *testing.T) {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")

	tests := []struct {
		name     string
		err1     error
		err2     error
		wantNil  bool
		wantText string
	}{
		{
			name:    "both nil",
			err1:    nil,
			err2:    nil,
			wantNil: true,
		},
		{
			name:     "first nil",
			err1:     nil,
			err2:     err2,
			wantNil:  false,
			wantText: "error 2",
		},
		{
			name:     "second nil",
			err1:     err1,
			err2:     nil,
			wantNil:  false,
			wantText: "error 1",
		},
		{
			name:     "both non-nil",
			err1:     err1,
			err2:     err2,
			wantNil:  false,
			wantText: "2 errors occurred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Append(tt.err1, tt.err2)

			if (err == nil) != tt.wantNil {
				t.Errorf("Append() nil = %v, want %v", err == nil, tt.wantNil)
				return
			}

			if err != nil && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("Append() error = %v, want to contain %v", err, tt.wantText)
			}
		})
	}
}

func TestMultiError_Is(t *testing.T) {
	targetErr := errors.New("target error")
	err1 := errors.New("error 1")
	err2 := targetErr
	err3 := errors.New("error 3")

	multiErr := Combine(err1, err2, err3)

	if !errors.Is(multiErr, targetErr) {
		t.Errorf("errors.Is() = false, want true")
	}

	otherErr := errors.New("other error")
	if errors.Is(multiErr, otherErr) {
		t.Errorf("errors.Is() = true, want false")
	}
}

// Define a custom error type for testing
type customError struct {
	msg string
}

// Make customError implement the error interface
func (e *customError) Error() string {
	return e.msg
}

func TestMultiError_As(t *testing.T) {
	targetErr := &customError{msg: "custom error"}
	err1 := errors.New("error 1")
	err2 := targetErr

	multiErr := Combine(err1, err2)

	var ce *customError
	if !errors.As(multiErr, &ce) {
		t.Errorf("errors.As() = false, want true")
	}

	if ce.msg != "custom error" {
		t.Errorf("ce.msg = %q, want %q", ce.msg, "custom error")
	}
}

func ExampleCombine() {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")

	err := Combine(err1, err2)
	fmt.Println(err)
	// Output:
	// 2 errors occurred:
	//   1: error 1
	//   2: error 2
}

func ExampleAppend() {
	var err error

	// Accumulate errors
	if _, err2 := fmt.Println("Operation 1"); err2 != nil {
		err = Append(err, err2)
	}

	if _, err2 := fmt.Println("Operation 2"); err2 != nil {
		err = Append(err, err2)
	}

	// Check if any errors occurred
	if err != nil {
		fmt.Println("Errors occurred:", err)
	} else {
		fmt.Println("All operations succeeded")
	}
	// Output:
	// Operation 1
	// Operation 2
	// All operations succeeded
}
