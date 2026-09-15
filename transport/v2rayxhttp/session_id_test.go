//go:build with_xhttp

package v2rayxhttp

import (
	"strings"
	"testing"
)

func TestSessionIDOptions(t *testing.T) {
	table, length, err := normalizeSessionIDOptions("number", "12-12")
	if err != nil {
		t.Fatal(err)
	}
	if table != "0123456789" || length != (intRange{12, 12}) {
		t.Fatalf("normalized options = %q, %#v", table, length)
	}
	id := newSessionID(table, length)
	if len(id) != 12 || strings.Trim(id, table) != "" {
		t.Fatalf("generated id %q does not match numeric length-12 contract", id)
	}
}

func TestDefaultSessionIDRemainsUUID(t *testing.T) {
	id := newSessionID("", intRange{})
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("default session id %q is not a dashed UUID", id)
	}
}

func TestSessionIDOptionsRejectInvalidInput(t *testing.T) {
	if _, _, err := normalizeSessionIDOptions("Base62", ""); err == nil {
		t.Fatal("expected a table without length to fail")
	}
	if _, _, err := normalizeSessionIDOptions("абв", "12"); err == nil {
		t.Fatal("expected a non-ASCII table to fail")
	}
	if _, _, err := normalizeSessionIDOptions("number", "1"); err == nil {
		t.Fatal("expected a session id space smaller than Xray's minimum to fail")
	}
}
