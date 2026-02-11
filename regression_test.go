package flux

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestPresetAtomRedirectFailureUpdatesTargetState(t *testing.T) {
	replaceErr := errors.New("replacement failed")

	original := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
	replacement := NewAtom(func(rc *ResolveContext) (int, error) { return 0, replaceErr })

	s := newTestScope(t, WithPresets(PresetAtom(original, replacement)))
	ctrl := GetController(s, original)

	var allEvents atomic.Int32
	var failedEvents atomic.Int32
	ctrl.On(EventAll, func() { allEvents.Add(1) })
	s.On(StateFailed, original, func() { failedEvents.Add(1) })

	_, err := Resolve(s, original)
	if !errors.Is(err, replaceErr) {
		t.Fatalf("expected replacement error, got %v", err)
	}

	if got := ctrl.State(); got != StateFailed {
		t.Fatalf("expected state failed, got %s", got)
	}

	if _, getErr := ctrl.Get(); !errors.Is(getErr, replaceErr) {
		t.Fatalf("expected controller get error %v, got %v", replaceErr, getErr)
	}

	if got := allEvents.Load(); got != 1 {
		t.Fatalf("expected 1 '*' controller event, got %d", got)
	}
	if got := failedEvents.Load(); got != 1 {
		t.Fatalf("expected 1 failed scope event, got %d", got)
	}
}
