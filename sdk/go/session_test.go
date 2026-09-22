package raildrop

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type conformanceFixture struct {
	Secret      string `json:"secret"`
	MetadataRaw string `json:"metadataRaw"`
	InputRaw    string `json:"inputRaw"`
	TSToken     string `json:"tsToken"`
	GoToken     string `json:"goToken"`
}

func loadConformanceFixture(t *testing.T) conformanceFixture {
	t.Helper()
	raw, err := os.ReadFile("../../test/fixtures/go-session-conformance.json")
	if err != nil {
		t.Fatalf("conformance fixture could not be read: %v", err)
	}
	var fixture conformanceFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("conformance fixture could not be parsed: %v", err)
	}
	return fixture
}

func conformancePayload(metadata string, input string) SessionPayload {
	return SessionPayload{
		ID:              "conformance-upload-id",
		Endpoint:        "avatar",
		Key:             "public/users/u_123/avatars/conformance-upload-id/file.png",
		File:            RequestedFile{Name: "photo.png", Size: 1234, Type: "image/png", LastModified: 1700000000000},
		Access:          AccessPublic,
		Retention:       RetentionPermanent,
		UploadExpiresAt: 4102444800000,
		Metadata:        json.RawMessage(metadata),
		Input:           json.RawMessage(input),
	}
}

func TestReadSessionTokenAcceptsTypeScriptTokens(t *testing.T) {
	fixture := loadConformanceFixture(t)
	payload, err := ReadSessionToken(fixture.TSToken, fixture.Secret)
	if err != nil {
		t.Fatalf("TypeScript-minted token was rejected: %v", err)
	}
	if payload.ID != "conformance-upload-id" ||
		payload.Endpoint != "avatar" ||
		payload.Key != "public/users/u_123/avatars/conformance-upload-id/file.png" ||
		payload.File.Name != "photo.png" ||
		payload.File.Size != 1234 ||
		payload.File.Type != "image/png" ||
		payload.File.LastModified != 1700000000000 ||
		payload.Access != AccessPublic ||
		payload.Retention != RetentionPermanent ||
		payload.UploadExpiresAt != 4102444800000 ||
		payload.Multipart != nil ||
		payload.ExpiresAt != nil {
		t.Fatalf("decoded payload does not match the fixture: %+v", payload)
	}
	if string(payload.Metadata) != string(fixture.MetadataRaw) {
		t.Fatalf("metadata bytes diverged: %s", string(payload.Metadata))
	}
	if string(payload.Input) != string(fixture.InputRaw) {
		t.Fatalf("input bytes diverged: %s", string(payload.Input))
	}
	var metadata map[string]string
	if err := json.Unmarshal(payload.Metadata, &metadata); err != nil {
		t.Fatalf("metadata could not be decoded: %v", err)
	}
	if metadata["userId"] != "u_123" || metadata["role"] != "admin" || metadata["2"] != "two" || metadata["10"] != "ten" {
		t.Fatalf("metadata values diverged: %v", metadata)
	}
	if metadata["note"] != "line\u2028sep\u2029end" {
		t.Fatalf("metadata line separators diverged: %q", metadata["note"])
	}
}

func TestReadSessionTokenAcceptsGoTokens(t *testing.T) {
	fixture := loadConformanceFixture(t)
	payload, err := ReadSessionToken(fixture.GoToken, fixture.Secret)
	if err != nil {
		t.Fatalf("Go-minted token was rejected: %v", err)
	}
	if payload.ID != "conformance-upload-id" || payload.Endpoint != "avatar" {
		t.Fatalf("decoded payload does not match the fixture: %+v", payload)
	}
	if string(payload.Metadata) != string(fixture.MetadataRaw) {
		t.Fatalf("metadata bytes diverged: %s", string(payload.Metadata))
	}
	var metadata map[string]string
	if err := json.Unmarshal(payload.Metadata, &metadata); err != nil {
		t.Fatalf("metadata could not be decoded: %v", err)
	}
	if metadata["note"] != "line\u2028sep\u2029end" {
		t.Fatalf("metadata line separators diverged: %q", metadata["note"])
	}
}

func TestCreateSessionTokenRoundTrips(t *testing.T) {
	fixture := loadConformanceFixture(t)
	token, err := CreateSessionToken(conformancePayload(fixture.MetadataRaw, fixture.InputRaw), fixture.Secret)
	if err != nil {
		t.Fatalf("token could not be minted: %v", err)
	}
	payload, err := ReadSessionToken(token, fixture.Secret)
	if err != nil {
		t.Fatalf("minted token was rejected: %v", err)
	}
	if payload.ID != "conformance-upload-id" || string(payload.Metadata) != string(fixture.MetadataRaw) {
		t.Fatalf("roundtrip diverged: %+v", payload)
	}
}

func TestSessionTokenRejectsWrongSecretAndTampering(t *testing.T) {
	fixture := loadConformanceFixture(t)
	if _, err := ReadSessionToken(fixture.TSToken, strings.Repeat("x", 32)); err == nil {
		t.Fatal("wrong secret was accepted")
	} else if raildropErr, ok := AsError(err); !ok || raildropErr.Code != CodeForbidden {
		t.Fatalf("wrong secret produced the wrong error: %v", err)
	}
	tampered := fixture.TSToken + "x"
	if _, err := ReadSessionToken(tampered, fixture.Secret); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestSessionTokenRejectsExpired(t *testing.T) {
	fixture := loadConformanceFixture(t)
	payload := conformancePayload(fixture.MetadataRaw, fixture.InputRaw)
	payload.UploadExpiresAt = 1
	token, err := CreateSessionToken(payload, fixture.Secret)
	if err != nil {
		t.Fatalf("token could not be minted: %v", err)
	}
	if _, err := ReadSessionToken(token, fixture.Secret); err == nil {
		t.Fatal("expired token was accepted")
	} else if raildropErr, ok := AsError(err); !ok || raildropErr.Code != CodeExpired {
		t.Fatalf("expiry produced the wrong error: %v", err)
	}
}

func TestSessionTokenRequiresStrongSecret(t *testing.T) {
	payload := conformancePayload(`{}`, `null`)
	if _, err := CreateSessionToken(payload, "short"); err == nil {
		t.Fatal("weak secret was accepted")
	}
	if _, err := ReadSessionToken("v1.a.b.c.d", "short"); err == nil {
		t.Fatal("weak secret was accepted for reads")
	}
}
