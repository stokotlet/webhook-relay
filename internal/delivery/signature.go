package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func Signature(secret, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret, id, timestamp string, body []byte, signature string) bool {
	return hmac.Equal([]byte(Signature(secret, id, timestamp, body)), []byte(signature))
}
