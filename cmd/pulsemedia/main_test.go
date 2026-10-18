package main

import (
	"testing"
)

func TestVersionDefaults(t *testing.T) {
	if Version == "" {
		t.Fatal("expected non-empty default Version")
	}
	if Commit == "" {
		t.Fatal("expected non-empty default Commit")
	}
	if Date == "" {
		t.Fatal("expected non-empty default Date")
	}
}
