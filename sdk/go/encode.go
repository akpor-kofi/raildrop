package raildrop

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

func fmtSscanf(value string, out *float64) error {
	_, err := fmt.Sscanf(value, "%g", out)
	return err
}

func pathUnescape(value string) (string, error) {
	return url.PathUnescape(value)
}

const encodeURIComponentUnreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"

func encodeURIComponent(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if strings.IndexByte(encodeURIComponentUnreserved, character) >= 0 {
			builder.WriteByte(character)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(strings.ToUpper(hex.EncodeToString([]byte{character}))[:2][0])
		builder.WriteByte(strings.ToUpper(hex.EncodeToString([]byte{character}))[:2][1])
	}
	return builder.String()
}

func base64RawURLEncode(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256Base64URL(secret string, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomUUID() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}
