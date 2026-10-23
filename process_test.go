package codexsdk

import (
	"errors"
	"testing"
)

func TestPublicOptionalProcessLifecycleBeforeStart(t *testing.T) {
	provider, ok := NewClient().(ProcessLifecycleProvider)
	if !ok {
		t.Fatal("missing optional process capability")
	}
	if _, err := provider.ProcessLifecycle(); !errors.Is(err, ErrProcessNotStarted) {
		t.Fatal(err)
	}
}
