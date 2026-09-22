package adapter

import (
	"strings"
	"testing"
)

func TestPatchWSLConfigAddsRequiredPersistenceWithoutDiscardingUserSettings(t *testing.T) {
	input := []byte("[wsl2]\r\nmemory=8GB\r\n\r\n[experimental]\r\nautoMemoryReclaim=gradual\r\n")
	patched, changed := patchWSLConfig(input)
	if !changed || !wslConfigHasPersistence(patched) {
		t.Fatalf("persistence settings were not added: %s", patched)
	}
	contents := string(patched)
	for _, expected := range []string{"memory=8GB", "autoMemoryReclaim=gradual", "instanceIdleTimeout=-1", "vmIdleTimeout=-1"} {
		if !strings.Contains(contents, expected) {
			t.Fatalf("patched config lost %q: %s", expected, contents)
		}
	}
	if !strings.Contains(contents, "\r\n") {
		t.Fatalf("patched config did not preserve CRLF newlines: %q", contents)
	}
}

func TestPatchWSLConfigReplacesFiniteTimeoutsAndIsIdempotent(t *testing.T) {
	input := []byte("[general]\ninstanceIdleTimeout=15000\n[wsl2]\nvmIdleTimeout = 60000\n")
	patched, changed := patchWSLConfig(input)
	if !changed || !wslConfigHasPersistence(patched) {
		t.Fatalf("finite persistence settings were not replaced: %s", patched)
	}
	again, changedAgain := patchWSLConfig(patched)
	if changedAgain || string(again) != string(patched) {
		t.Fatalf("persistence patch was not idempotent: %q != %q", again, patched)
	}
}
