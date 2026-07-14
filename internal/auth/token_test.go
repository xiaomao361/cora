package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBearerTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := LoadBearerTokenFile(path)
	if err != nil || token != "secret-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if err := os.WriteFile(path, []byte("two words"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBearerTokenFile(path); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("error=%v", err)
	}
}
