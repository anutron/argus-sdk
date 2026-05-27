// Package testutil holds the assertion helpers the SDK's own tests use.
// It is internal: not part of the SDK's public surface. Plugins should
// bring their own assertion library.
package testutil

import (
	"reflect"
	"strings"
	"testing"
)

// Equal asserts got == want for comparable types.
func Equal[T comparable](t testing.TB, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

// NotNil asserts that got is not nil, handling the typed-nil-interface trap.
func NotNil(t testing.TB, got any) {
	t.Helper()
	if got == nil {
		t.Errorf("got nil, want non-nil")
		return
	}
	v := reflect.ValueOf(got)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			t.Errorf("got typed nil (%T), want non-nil", got)
		}
	}
}

// Contains asserts that s contains substr.
func Contains(t testing.TB, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("string %q does not contain %q", s, substr)
	}
}
