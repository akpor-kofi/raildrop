package raildrop

import (
	"encoding/json"
	"testing"
)

func TestMarshalJSONJavaScriptMatchesV8Serialization(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		{"string keys stay in map order", map[string]any{"b": 1, "a": 2}, `{"a":2,"b":1}`},
		{
			"integer-like keys sort first and numerically",
			map[string]any{"z": 1, "10": 2, "2": 3},
			`{"2":3,"10":2,"z":1}`,
		},
		{
			"line separators stay raw",
			map[string]any{"note": "line\u2028sep\u2029end"},
			"{\"note\":\"line\u2028sep\u2029end\"}",
		},
		{"html characters stay raw", map[string]any{"tag": "<b>&amp;</b>"}, `{"tag":"<b>&amp;</b>"}`},
		{"control characters escape", map[string]any{"c": "a\x01b"}, `{"c":"a\u0001b"}`},
		{"nil metadata omits to null", nil, `null`},
		{"arrays preserve order", []any{"10", "2"}, `["10","2"]`},
		{"nested objects normalize", map[string]any{"outer": map[string]any{"9": 1, "a": 2}}, `{"outer":{"9":1,"a":2}}`},
	}
	for _, testCase := range cases {
		actual, err := MarshalJSONJavaScript(testCase.value)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if string(actual) != testCase.expected {
			t.Fatalf("%s: got %s; want %s", testCase.name, string(actual), testCase.expected)
		}
		var roundTrip any
		if err := json.Unmarshal(actual, &roundTrip); err != nil {
			t.Fatalf("%s: output is not valid JSON: %v", testCase.name, err)
		}
	}
}

func TestMarshalJSONJavaScriptStructFields(t *testing.T) {
	type metadata struct {
		UserID string `json:"userId"`
		Role   string `json:"role,omitempty"`
		Skip   string `json:"-"`
	}
	actual, err := MarshalJSONJavaScript(metadata{UserID: "u_1", Role: "", Skip: "hidden"})
	if err != nil {
		t.Fatalf("struct metadata could not be serialized: %v", err)
	}
	if string(actual) != `{"userId":"u_1"}` {
		t.Fatalf("struct serialization diverged: %s", string(actual))
	}
}

func TestMetadataDigestTreatsAbsentMetadataAsUndefined(t *testing.T) {
	absent := MetadataDigest(nil)
	if absent != MetadataDigest(json.RawMessage(nil)) {
		t.Fatal("nil and empty metadata digests must agree")
	}
	explicitNull := MetadataDigest(json.RawMessage("null"))
	if explicitNull == absent {
		t.Fatal("explicit null must not collide with undefined")
	}
	payloadAbsent := SessionPayload{ID: "x", Metadata: nil, UploadExpiresAt: 4102444800000}
	token, err := CreateSessionToken(payloadAbsent, "test-secret-that-is-at-least-thirty-two-characters")
	if err != nil {
		t.Fatalf("token could not be minted: %v", err)
	}
	decoded, err := ReadSessionToken(token, "test-secret-that-is-at-least-thirty-two-characters")
	if err != nil {
		t.Fatalf("token with absent metadata was rejected: %v", err)
	}
	if len(decoded.Metadata) != 0 {
		t.Fatalf("absent metadata did not round-trip: %s", string(decoded.Metadata))
	}
}

func TestValidateRouteRulesRejectsMissingSize(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("missing MaxFileSize must fail fast")
		}
	}()
	DefineRoute(Rules{"image": {MaxFileCount: 1}}, RouteDefinition[any, map[string]string, map[string]any]{})
}
