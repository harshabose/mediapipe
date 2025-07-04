# multierr

The `multierr` package provides utilities for combining multiple errors into a single error.

## Usage

### Combining Multiple Errors

Use the `Combine` function to combine multiple errors into a single error:

```go
package main
import "github.com/harshabose/mediasink/internal/utils/multierr"

func doSomething() error {
    var errs []error
    
    if err := step1(); err != nil {
        errs = append(errs, err)
    }
    
    if err := step2(); err != nil {
        errs = append(errs, err)
    }
    
    if err := step3(); err != nil {
        errs = append(errs, err)
    }
    
    // Combine all errors into a single error
    return multierr.Combine(errs...)
}
```

If all errors are nil, `Combine` returns nil.
If there's only one non-nil error, it returns that error.
If there are multiple non-nil errors, it combines them into a single error.

### Appending Errors

Use the `Append` function to combine two errors:

```go
package main
import "github.com/harshabose/mediasink/internal/utils/multierr"

func processItems(items []Item) error {
    var err error
    
    for _, item := range items {
        if e := processItem(item); e != nil {
            err = multierr.Append(err, e)
        }
    }
    
    return err
}
```

If both errors are nil, `Append` returns nil.
If one error is nil, it returns the non-nil error.
If both errors are non-nil, it combines them into a single error.

### Error Handling

The combined error supports standard Go error handling mechanisms:

```go
import (
    "errors"
    "github.com/harshabose/mediasink/internal/utils/multierr"
)

var ErrNotFound = errors.New("not found")

func checkError(err error) {
    // Check if the combined error contains a specific error
    if errors.Is(err, ErrNotFound) {
        // Handle not found error
    }
    
    // Type assertion with errors.As
    var customErr *CustomError
    if errors.As(err, &customErr) {
        // Handle custom error
    }
}
```

## Error Formatting

When a combined error is converted to a string, it formats the errors in a readable way:

```
3 errors occurred:
  1: first error message
  2: second error message
  3: third error message
```

This makes it easy to identify all the errors that occurred.