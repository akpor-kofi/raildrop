package raildrop

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SessionPayload struct {
	Version         int               `json:"version"`
	ID              string            `json:"id"`
	Endpoint        string            `json:"endpoint"`
	Key             string            `json:"key"`
	File            RequestedFile     `json:"file"`
	Access          Access            `json:"access"`
	Retention       Retention         `json:"retention"`
	ExpiresAt       *string           `json:"expiresAt"`
	UploadExpiresAt int64             `json:"uploadExpiresAt"`
	MetadataDigest  string            `json:"metadataDigest"`
	Metadata        json.RawMessage   `json:"metadata"`
	Input           json.RawMessage   `json:"input"`
	Multipart       *MultipartSession `json:"multipart"`
}

type MultipartSession struct {
	UploadID string `json:"uploadId"`
}

const (
	sessionAAD            = "raildrop-session-v1"
	sessionEncryptionInfo = "raildrop:session:encryption:v1\x00"
)

func MarshalJSON(value any) (json.RawMessage, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buffer.Bytes(), "\n")), nil
}

func CompactJSON(value []byte) (json.RawMessage, error) {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, value); err != nil {
		return nil, err
	}
	compact, err := MarshalJSON(json.RawMessage(buffer.Bytes()))
	if err != nil {
		return nil, err
	}
	return compact, nil
}

func MetadataDigest(metadata json.RawMessage) string {
	raw, _ := MarshalJSON(struct {
		Metadata json.RawMessage `json:"metadata"`
	}{metadata})
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sessionEncryptionKey(secret string) []byte {
	sum := sha256.Sum256([]byte(sessionEncryptionInfo + secret))
	return sum[:]
}

func SafeEqual(actual string, expected string) bool {
	actualBytes, err := base64.RawURLEncoding.DecodeString(actual)
	if err != nil {
		return false
	}
	expectedBytes, err := base64.RawURLEncoding.DecodeString(expected)
	if err != nil {
		return false
	}
	return hmac.Equal(actualBytes, expectedBytes)
}

func CreateSessionToken(payload SessionPayload, secret string) (string, error) {
	if len(secret) < 32 {
		return "", fmt.Errorf("RAILDROP_SECRET must contain at least 32 characters")
	}
	payload.Version = 1
	payload.MetadataDigest = MetadataDigest(payload.Metadata)
	rawPayload, err := MarshalJSON(payload)
	if err != nil {
		return "", WrapError(CodeBadRequest, "Session payload could not be serialized.", err)
	}
	block, err := aes.NewCipher(sessionEncryptionKey(secret))
	if err != nil {
		return "", WrapError(CodeStorageError, "Session encryption failed.", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", WrapError(CodeStorageError, "Session encryption failed.", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", WrapError(CodeStorageError, "Session encryption failed.", err)
	}
	aad := []byte(sessionAAD)
	sealed := gcm.Seal(nil, iv, rawPayload, aad)
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	tag := sealed[len(sealed)-gcm.Overhead():]
	unsigned := fmt.Sprintf(
		"v1.%s.%s.%s",
		base64.RawURLEncoding.EncodeToString(iv),
		base64.RawURLEncoding.EncodeToString(ciphertext),
		base64.RawURLEncoding.EncodeToString(tag),
	)
	signature := hmacSHA256Base64URL(secret, unsigned)
	return unsigned + "." + signature, nil
}

func ReadSessionToken(token string, secret string) (*SessionPayload, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("RAILDROP_SECRET must contain at least 32 characters")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "v1" {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	expectedSignature := hmacSHA256Base64URL(secret, strings.Join(parts[:4], "."))
	if !SafeEqual(parts[4], expectedSignature) {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	block, err := aes.NewCipher(sessionEncryptionKey(secret))
	if err != nil {
		return nil, NewError(CodeStorageError, "Session decryption failed.")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, NewError(CodeStorageError, "Session decryption failed.")
	}
	plaintext, err := gcm.Open(nil, iv, append(append([]byte{}, ciphertext...), tag...), []byte(sessionAAD))
	if err != nil {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	var payload SessionPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	if payload.Version != 1 {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	if time.Now().UnixMilli() > payload.UploadExpiresAt {
		return nil, NewError(CodeExpired, "Upload session expired.")
	}
	if !SafeEqual(payload.MetadataDigest, MetadataDigest(payload.Metadata)) {
		return nil, NewError(CodeForbidden, "Invalid upload session.")
	}
	return &payload, nil
}

func ISOString(timeValue time.Time) string {
	return timeValue.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func ParseISOString(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
