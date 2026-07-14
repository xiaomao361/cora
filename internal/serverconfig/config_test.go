package serverconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadKeepsPathsRelativeToWorkingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cora.yml")
	data := `server:
  http_listen_address: 10.0.0.10
  http_listen_port: 8181
storage:
  path: ./cora.db
auth:
  bearer_token_file: ./auth.token
aggregation:
  flush_interval: 2s
  max_active: 123
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Address() != "10.0.0.10:8181" || runtime.DatabasePath != "./cora.db" ||
		runtime.BearerTokenFile != "./auth.token" || runtime.FlushInterval != 2*time.Second ||
		runtime.MaxActive != 123 {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestLoadRejectsUnknownFieldsAndMissingAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, data, want string
	}{
		{name: "unknown", data: "server:\n  unknown: true\nauth:\n  allow_unauthenticated: true\n", want: "field unknown not found"},
		{name: "auth", data: "server:\n  http_listen_port: 8080\n", want: "bearer_token_file is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cora.yml")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}
