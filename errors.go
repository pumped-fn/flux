package flux

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotResolved = errors.New("flux: atom not resolved — call Resolve before accessing value")
	ErrClosed      = errors.New("flux: exec context is closed")
	ErrScopeClosed = errors.New("flux: scope has been disposed")
)

type CircularDepError struct {
	Path []uint64
}

func (e *CircularDepError) Error() string {
	parts := make([]string, len(e.Path))
	for i, id := range e.Path {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return fmt.Sprintf("circular dependency detected: %s", strings.Join(parts, " -> "))
}

type ParseError struct {
	Phase string
	Label string
	Cause error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("failed to parse %s %q: %v", e.Phase, e.Label, e.Cause)
}

func (e *ParseError) Unwrap() error {
	return e.Cause
}

type InvalidationLoopError struct {
	Path []string
}

func (e *InvalidationLoopError) Error() string {
	return fmt.Sprintf("infinite invalidation loop detected: %s", strings.Join(e.Path, " -> "))
}

func asError(recovered any) error {
	if recovered == nil {
		return nil
	}
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", recovered)
}
