package raildrop

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMintConformanceToken(t *testing.T) {
	if os.Getenv("RAILDROP_MINT_CONFORMANCE_TOKEN") == "" {
		t.Skip("set RAILDROP_MINT_CONFORMANCE_TOKEN to mint a conformance token")
	}
	fixturePath := os.Getenv("RAILDROP_CONFORMANCE_FIXTURE")
	if fixturePath == "" {
		fixturePath = "../../test/fixtures/go-session-conformance.json"
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("fixture could not be read: %v", err)
	}
	var fixture struct {
		Secret      string `json:"secret"`
		MetadataRaw string `json:"metadataRaw"`
		InputRaw    string `json:"inputRaw"`
		GoToken     string `json:"goToken"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("fixture could not be parsed: %v", err)
	}
	payload := SessionPayload{
		ID:              "conformance-upload-id",
		Endpoint:        "avatar",
		Key:             "public/users/u_123/avatars/conformance-upload-id/file.png",
		File:            RequestedFile{Name: "photo.png", Size: 1234, Type: "image/png", LastModified: 1700000000000},
		Access:          AccessPublic,
		Retention:       RetentionPermanent,
		UploadExpiresAt: 4102444800000,
		Metadata:        json.RawMessage(fixture.MetadataRaw),
		Input:           json.RawMessage(fixture.InputRaw),
	}
	token, err := CreateSessionToken(payload, fixture.Secret)
	if err != nil {
		t.Fatalf("token could not be minted: %v", err)
	}
	_, err = os.Stdout.WriteString(token + "\n")
	if err != nil {
		t.Fatalf("token could not be written: %v", err)
	}
}
