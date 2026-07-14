package sanitize

import (
	"strings"
	"testing"
)

func TestRedactSignedURLCredentials(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		secrets []string
		markers int
	}{
		{
			name:    "OSS",
			input:   `download https://bucket.oss-cn.example/a.pdf?OSSAccessKeyId=key-id&Expires=1720000000&Signature=encoded%2Bsignature&response-content-type=application/pdf`,
			secrets: []string{"key-id", "1720000000", "encoded%2Bsignature"},
			markers: 3,
		},
		{
			name:    "AWS",
			input:   `https://bucket.s3.example/a?X-Amz-Credential=credential%2Fscope&X-Amz-Date=20260714T000000Z&X-Amz-Expires=900&X-Amz-Security-Token=session-token&X-Amz-Signature=signature&partNumber=1`,
			secrets: []string{"credential%2Fscope", "20260714T000000Z", "900", "session-token", "signature"},
			markers: 5,
		},
		{
			name:    "OSS V4",
			input:   `https://bucket.oss-cn.example/a?x-oss-credential=credential%2Fscope&x-oss-date=20260714T000000Z&x-oss-expires=900&x-oss-security-token=session-token&x-oss-signature=secret-value&x-oss-process=image/resize,w_100`,
			secrets: []string{"credential%2Fscope", "20260714T000000Z", "900", "session-token", "secret-value"},
			markers: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RedactSignedURLCredentials(test.input)
			for _, secret := range test.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("secret %q leaked in %q", secret, got)
				}
			}
			if count := strings.Count(got, "[REDACTED]"); count != test.markers {
				t.Fatalf("markers=%d, want %d: %q", count, test.markers, got)
			}
			if !strings.Contains(got, "response-content-type=application/pdf") &&
				!strings.Contains(got, "partNumber=1") &&
				!strings.Contains(got, "x-oss-process=image/resize,w_100") {
				t.Fatalf("ordinary query parameter changed: %q", got)
			}
		})
	}
}

func TestRedactSignedURLCredentialsLeavesOrdinaryTextUnchanged(t *testing.T) {
	input := "cache expires=soon and signature=diagnostic"
	if got := RedactSignedURLCredentials(input); got != input {
		t.Fatalf("got %q, want unchanged %q", got, input)
	}
}
