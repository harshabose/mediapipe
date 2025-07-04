// Package multierr provides utilities for combining multiple errors into a single error.
package multierr

import (
	"errors"
	"fmt"
	"strings"
)

// Combine takes a list of errors and combines them into a single error.
// If all errors are nil, it returns nil.
// If there's only one non-nil error, it returns that error.
// If there are multiple non-nil errors, it combines them into a single error.
func Combine(errs ...error) error {
	var nonNilErrs []error
	for _, err := range errs {
		if err != nil {
			nonNilErrs = append(nonNilErrs, err)
		}
	}

	if len(nonNilErrs) == 0 {
		return nil
	}

	if len(nonNilErrs) == 1 {
		return nonNilErrs[0]
	}

	return &multiError{
		errors: nonNilErrs,
	}
}

// Append combines the two errors into a single error.
// If both errors are nil, it returns nil.
// If one error is nil, it returns the non-nil error.
// If both errors are non-nil, it combines them into a single error.
func Append(err1, err2 error) error {
	if err1 == nil {
		return err2
	}

	if err2 == nil {
		return err1
	}

	// If err1 is already a multiError, append err2 to it
	if me, ok := err1.(*multiError); ok {
		return &multiError{
			errors: append(me.errors, err2),
		}
	}

	// If err2 is already a multiError, prepend err1 to it
	if me, ok := err2.(*multiError); ok {
		return &multiError{
			errors: append([]error{err1}, me.errors...),
		}
	}

	// Neither error is a multiError, create a new one
	return &multiError{
		errors: []error{err1, err2},
	}
}

// multiError is an implementation of error that contains multiple errors.
type multiError struct {
	errors []error
}

// Error returns a string representation of all the errors.
func (m *multiError) Error() string {
	if len(m.errors) == 0 {
		return ""
	}

	if len(m.errors) == 1 {
		return m.errors[0].Error()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d errors occurred:\n", len(m.errors)))
	for i, err := range m.errors {
		sb.WriteString(fmt.Sprintf("  %d: %s\n", i+1, err.Error()))
	}
	return sb.String()
}

// Unwrap returns the underlying errors.
// This allows errors.Is and errors.As to work with multiError.
func (m *multiError) Unwrap() []error {
	return m.errors
}

// Is reports whether any error in multiError matches target.
func (m *multiError) Is(target error) bool {
	for _, err := range m.errors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// As finds the first error in multiError that matches target, and if one is found, sets
// target to that error value and returns true. Otherwise, it returns false.
func (m *multiError) As(target interface{}) bool {
	for _, err := range m.errors {
		if errors.As(err, target) {
			return true
		}
	}
	return false
}
