package controlplane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pluginToken is a short-lived HMAC-signed credential Core issues to a plugin on
// register. The plugin presents it on heartbeat/deregister so Core can verify the
// caller actually completed registration (not just anyone with the api_key).
//
// Format: base64(pluginID "." expiryUnix "." hmac)
type tokenSigner struct {
	secret []byte
	ttl    time.Duration
}

func newTokenSigner(secret string, ttl time.Duration) *tokenSigner {
	return &tokenSigner{secret: []byte(secret), ttl: ttl}
}

func (s *tokenSigner) issue(pluginID string) string {
	exp := time.Now().Add(s.ttl).Unix()
	payload := pluginID + "." + strconv.FormatInt(exp, 10)
	sig := s.sign(payload)
	raw := payload + "." + sig
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// verify checks the token's signature and expiry and returns the plugin ID.
func (s *tokenSigner) verify(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("malformed token")
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed token")
	}
	pluginID, expStr, sig := parts[0], parts[1], parts[2]
	if !hmac.Equal([]byte(sig), []byte(s.sign(pluginID+"."+expStr))) {
		return "", fmt.Errorf("invalid signature")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}
	return pluginID, nil
}

func (s *tokenSigner) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
