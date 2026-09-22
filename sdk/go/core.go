package raildrop

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

type Access string

const (
	AccessPublic  Access = "public"
	AccessPrivate Access = "private"
)

type Retention string

const (
	RetentionPermanent Retention = "permanent"
	RetentionTemporary Retention = "temporary"
)

type FileType string

const (
	TypeImage FileType = "image"
	TypeVideo FileType = "video"
	TypeAudio FileType = "audio"
	TypePDF   FileType = "pdf"
	TypeText  FileType = "text"
	TypeBlob  FileType = "blob"
)

type RequestedFile struct {
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Type         string `json:"type"`
	LastModified int64  `json:"lastModified,omitempty"`
}

const (
	KB = 1_000
	MB = 1_000_000
	GB = 1_000_000_000
)

var sizePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)(KB|MB|GB)$`)

func ParseFileSize(value string) (int64, error) {
	match := sizePattern.FindStringSubmatch(value)
	if match == nil {
		return 0, NewError(CodeBadRequest, "Invalid file size: "+value)
	}
	var amount float64
	if err := fmtSscanf(match[1], &amount); err != nil {
		return 0, NewError(CodeBadRequest, "Invalid file size: "+value)
	}
	var factor int64
	switch match[2] {
	case "KB":
		factor = KB
	case "MB":
		factor = MB
	default:
		factor = GB
	}
	return int64(amount * float64(factor)), nil
}

var mimeGroupPattern = map[FileType]string{
	TypeImage: "image/",
	TypeVideo: "video/",
	TypeAudio: "audio/",
	TypePDF:   "application/pdf",
	TypeText:  "text/",
}

func MimeGroupFor(contentType string) FileType {
	for fileType, prefix := range mimeGroupPattern {
		if fileType == TypePDF {
			if contentType == prefix {
				return fileType
			}
			continue
		}
		if strings.HasPrefix(contentType, prefix) {
			return fileType
		}
	}
	return TypeBlob
}

var fileExtensionPattern = regexp.MustCompile(`(?i)\.([a-z0-9]{1,12})$`)

func FileExtensionFor(name string, contentType string) string {
	if match := fileExtensionPattern.FindStringSubmatch(name); match != nil {
		return strings.ToLower(match[1])
	}
	subtype := strings.Split(contentType, "/")
	if len(subtype) == 2 {
		cleaned := strings.Split(subtype[1], "+")[0]
		cleaned = regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(cleaned), "")
		if cleaned != "" {
			return cleaned
		}
	}
	return "bin"
}

func hasControlCharacter(value string) bool {
	for _, character := range value {
		if character <= 31 || character == 127 {
			return true
		}
	}
	return false
}

func Namespace(segments ...string) ([]string, error) {
	if len(segments) == 0 {
		return nil, NewError(CodeBadRequest, "A namespace must contain at least one segment.")
	}
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		value := strings.Trim(segment, " \t\n\r\v\f")
		if value == "" || value == "." || value == ".." ||
			strings.ContainsAny(value, "/\\") || hasControlCharacter(value) {
			return nil, NewError(CodeBadRequest, "Invalid namespace segment: "+segment)
		}
		decoded, err := pathUnescape(value)
		if err != nil || decoded != value {
			return nil, NewError(CodeBadRequest, "Namespace segments must not be pre-encoded.")
		}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func EncodeNamespace(segments ...string) (string, error) {
	normalized, err := Namespace(segments...)
	if err != nil {
		return "", err
	}
	encoded := make([]string, 0, len(normalized))
	for _, segment := range normalized {
		encoded = append(encoded, encodeURIComponent(segment))
	}
	return strings.Join(encoded, "/"), nil
}

func IsPublicObjectKey(key string) bool {
	return strings.HasPrefix(key, "public/") &&
		!strings.Contains(key, "..") &&
		!strings.Contains(key, "\\")
}

func SanitizeDownloadName(name string) string {
	normalized := norm.NFKC.String(name)
	var builder strings.Builder
	for _, character := range normalized {
		if character <= 31 || character == 127 ||
			character == '"' || character == '\\' || character == '/' {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(character)
	}
	result := strings.Trim(builder.String(), " \t\n\r\v\f")
	runes := []rune(result)
	if len(runes) > 180 {
		result = string(runes[:180])
	}
	if result == "" {
		return "download"
	}
	return result
}

func Base64EncodeName(name string) string {
	return base64RawURLEncode([]byte(SanitizeDownloadName(name)))
}
