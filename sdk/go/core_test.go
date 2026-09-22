package raildrop

import (
	"strings"
	"testing"
)

func TestParseFileSize(t *testing.T) {
	cases := []struct {
		value    string
		expected int64
	}{
		{"4MB", 4_000_000},
		{"1KB", 1_000},
		{"2.5GB", 2_500_000_000},
		{"1.5MB", 1_500_000},
	}
	for _, testCase := range cases {
		actual, err := ParseFileSize(testCase.value)
		if err != nil || actual != testCase.expected {
			t.Fatalf("ParseFileSize(%q) = %d, %v; want %d", testCase.value, actual, err, testCase.expected)
		}
	}
	for _, invalid := range []string{"", "4", "4kb", "4 MB", "-4MB"} {
		if _, err := ParseFileSize(invalid); err == nil {
			t.Fatalf("ParseFileSize(%q) should fail", invalid)
		}
	}
}

func TestEncodeNamespaceMatchesJavaScriptEncodeURIComponent(t *testing.T) {
	cases := []struct {
		segments []string
		expected string
	}{
		{[]string{"users", "u_1", "avatars"}, "users/u_1/avatars"},
		{[]string{"hello world", "a+b", "c&d"}, "hello%20world/a%2Bb/c%26d"},
		{[]string{"ünïcode", "señor"}, "%C3%BCn%C3%AFcode/se%C3%B1or"},
		{[]string{"not!encoded~.()'*", "keep"}, "not!encoded~.()'*/keep"},
	}
	for _, testCase := range cases {
		actual, err := EncodeNamespace(testCase.segments...)
		if err != nil {
			t.Fatalf("EncodeNamespace(%v) failed: %v", testCase.segments, err)
		}
		if actual != testCase.expected {
			t.Fatalf("EncodeNamespace(%v) = %q; want %q", testCase.segments, actual, testCase.expected)
		}
	}
	for _, invalid := range [][]string{
		{"a/b"},
		{"a\\b"},
		{""},
		{"."},
		{".."},
		{"hello%20world"},
		{"%zz"},
		{"50%"},
		{"control\x01"},
		{},
	} {
		if _, err := EncodeNamespace(invalid...); err == nil {
			t.Fatalf("EncodeNamespace(%v) should fail", invalid)
		}
	}
}

func TestNamespaceRoundTrip(t *testing.T) {
	if _, err := Namespace("H%C3%A9llo"); err == nil {
		t.Fatal("pre-encoded segments must be rejected")
	}
	if _, err := Namespace("Héllo", "World"); err != nil {
		t.Fatalf("unicode segments must be accepted: %v", err)
	}
}

func TestSanitizeDownloadName(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"photo.png", "photo.png"},
		{`bad/"name"\.png`, "bad__name__.png"},
		{"  spaced  ", "spaced"},
		{"", "download"},
		{"café", "café"},
		{strings.Repeat("a", 300), strings.Repeat("a", 180)},
		{"control\x07char", "control_char"},
	}
	for _, testCase := range cases {
		if actual := SanitizeDownloadName(testCase.input); actual != testCase.expected {
			t.Fatalf("SanitizeDownloadName(%q) = %q; want %q", testCase.input, actual, testCase.expected)
		}
	}
	if Base64EncodeName("photo.png") != "cGhvdG8ucG5n" {
		t.Fatalf("Base64EncodeName diverged from the TypeScript fixture: %s", Base64EncodeName("photo.png"))
	}
}

func TestMimeGroupFor(t *testing.T) {
	cases := []struct {
		contentType string
		expected    FileType
	}{
		{"image/png", TypeImage},
		{"video/mp4", TypeVideo},
		{"audio/wav", TypeAudio},
		{"application/pdf", TypePDF},
		{"text/plain", TypeText},
		{"application/octet-stream", TypeBlob},
		{"application/json", TypeBlob},
	}
	for _, testCase := range cases {
		if actual := MimeGroupFor(testCase.contentType); actual != testCase.expected {
			t.Fatalf("MimeGroupFor(%q) = %q; want %q", testCase.contentType, actual, testCase.expected)
		}
	}
}

func TestFileExtensionFor(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		expected    string
	}{
		{"photo.PNG", "image/png", "png"},
		{"noextension", "image/jpeg", "jpeg"},
		{"file.tar.gz", "application/gzip", "gz"},
		{"weird name", "application/vnd.x+y+z", "vndx"},
		{"weird name", "weird", "bin"},
	}
	for _, testCase := range cases {
		if actual := FileExtensionFor(testCase.name, testCase.contentType); actual != testCase.expected {
			t.Fatalf("FileExtensionFor(%q, %q) = %q; want %q", testCase.name, testCase.contentType, actual, testCase.expected)
		}
	}
}

func TestIsPublicObjectKey(t *testing.T) {
	if !IsPublicObjectKey("public/a/b") {
		t.Fatal("public keys must be public")
	}
	if IsPublicObjectKey("private/a/b") {
		t.Fatal("private keys must not be public")
	}
	if IsPublicObjectKey("public/../private") {
		t.Fatal("traversal keys must not be public")
	}
}
