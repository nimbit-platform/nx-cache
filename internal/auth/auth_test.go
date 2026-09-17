package auth

import (
	"net/http"
	"testing"
)

func TestBearerOK(t *testing.T) {
	ok, status, _ := BearerOK("", "write", "read", true)
	if ok || status != http.StatusUnauthorized {
		t.Fatalf("empty header: ok=%v status=%d", ok, status)
	}
	ok, status, _ = BearerOK("Bearer write", "write", "read", true)
	if !ok {
		t.Fatal("write token should allow put")
	}
	ok, status, msg := BearerOK("Bearer read", "write", "read", true)
	if ok || status != http.StatusForbidden {
		t.Fatalf("read token write: ok=%v status=%d msg=%s", ok, status, msg)
	}
	ok, status, _ = BearerOK("Bearer read", "write", "read", false)
	if !ok {
		t.Fatal("read token should allow get")
	}
	ok, status, _ = BearerOK("Bearer nope", "write", "read", false)
	if ok || status != http.StatusUnauthorized {
		t.Fatalf("wrong token: ok=%v status=%d", ok, status)
	}
}

func TestCheckPassword(t *testing.T) {
	if !CheckPassword("secret", "secret") {
		t.Fatal("same password")
	}
	if CheckPassword("secret", "other") {
		t.Fatal("different password")
	}
	if CheckPassword("", "x") {
		t.Fatal("empty vs set")
	}
}
