package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

func LoadBearerTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bearer token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("bearer token file is empty")
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", errors.New("bearer token must not contain whitespace")
	}
	return token, nil
}
