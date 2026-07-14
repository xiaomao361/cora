package sanitize

import "regexp"

var signedURLCredentialPattern = regexp.MustCompile(`(?i)([?&](?:OSSAccessKeyId|Signature|Expires|security-token|x-oss-credential|x-oss-signature|x-oss-security-token|x-oss-date|x-oss-expires|X-Amz-Credential|X-Amz-Signature|X-Amz-Security-Token|X-Amz-Date|X-Amz-Expires)=)[^&#\s"'<>]+`)

// RedactSignedURLCredentials removes credentials from common OSS and S3 signed
// URL query strings while preserving the URL shape for diagnosis.
func RedactSignedURLCredentials(value string) string {
	return signedURLCredentialPattern.ReplaceAllString(value, `${1}[REDACTED]`)
}
