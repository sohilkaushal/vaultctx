//go:build linux

package app

import (
	"syscall"
	"testing"
)

func TestProcessGroupActivePreservesPermissionDenied(t *testing.T) {
	active, err := processGroupActiveAfterProbe(1<<30, syscall.EPERM)
	if err != nil {
		t.Fatalf("processGroupActive() error = %v", err)
	}
	if !active {
		t.Fatal("processGroupActive() = false after EPERM probe, want true")
	}
}

func TestLinuxProcessGroupState(t *testing.T) {
	for _, testCase := range []struct {
		stat  string
		group int
		state byte
		ok    bool
	}{
		{stat: "123 (vaultctx helper) S 1 456 456 0", group: 456, state: 'S', ok: true},
		{stat: "123 (name with ) paren) Z 1 789 789 0", group: 789, state: 'Z', ok: true},
		{stat: "malformed", ok: false},
	} {
		group, state, ok := linuxProcessGroupState([]byte(testCase.stat))
		if group != testCase.group || state != testCase.state || ok != testCase.ok {
			t.Errorf("linuxProcessGroupState(%q) = (%d, %q, %v), want (%d, %q, %v)", testCase.stat, group, state, ok, testCase.group, testCase.state, testCase.ok)
		}
	}
}
