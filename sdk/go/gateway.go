package raildrop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const GlobalManifestKey = "public/global/_raildrop/manifest.json"

type PublicGatewayConfig struct {
	Storage Storage
	Prefix  string
}

func noStoreText(w http.ResponseWriter, body string, status int) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if status >= 200 && status != 204 && status != 304 {
		_, _ = io.WriteString(w, body)
	}
}

func readGatewayKey(pathname string, prefix string) (string, bool) {
	if prefix != "" && !strings.HasPrefix(pathname, prefix) {
		return "", false
	}
	encodedPath := strings.TrimLeft(pathname[len(prefix):], "/")
	if encodedPath == "" {
		return "", false
	}
	encodedSegments := strings.Split(encodedPath, "/")
	for _, segment := range encodedSegments {
		if segment == "" {
			return "", false
		}
	}
	decodedSegments := make([]string, len(encodedSegments))
	for index, segment := range encodedSegments {
		decoded, err := pathUnescape(segment)
		if err != nil {
			return "", false
		}
		decodedSegments[index] = decoded
	}
	normalizedSegments, err := Namespace(decodedSegments...)
	if err != nil {
		return "", false
	}
	for index, segment := range normalizedSegments {
		if segment != decodedSegments[index] {
			return "", false
		}
	}
	encoded, err := EncodeNamespace(normalizedSegments...)
	if err != nil {
		return "", false
	}
	return encoded, true
}

func unsafeInlineType(contentType *string) bool {
	if contentType == nil {
		return false
	}
	switch *contentType {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "application/javascript", "text/javascript":
		return true
	default:
		return false
	}
}

func metadataDownloadName(metadata map[string]string) (string, bool) {
	encoded, ok := metadata["raildrop-name"]
	if !ok || encoded == "" {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

var globalAliasPattern = regexp.MustCompile(`^global/([^/]+)$`)

func resolveAlias(storage Storage, alias string) (string, error) {
	object, err := storage.Get(context.Background(), GlobalManifestKey, "")
	if err != nil {
		return "", err
	}
	if object == nil {
		return "", nil
	}
	defer object.Close()
	body, err := io.ReadAll(object.Body)
	if err != nil {
		return "", nil
	}
	var manifest struct {
		Version *int `json:"version"`
		Assets  map[string]struct {
			Key string `json:"key"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", nil
	}
	if manifest.Version == nil || *manifest.Version != 1 {
		return "", nil
	}
	entry, ok := manifest.Assets[alias]
	if !ok {
		return "", nil
	}
	return entry.Key, nil
}

func NewPublicGateway(config PublicGatewayConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			noStoreText(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key, ok := readGatewayKey(req.URL.EscapedPath(), config.Prefix)
		if !ok {
			noStoreText(w, "Invalid asset path", http.StatusBadRequest)
			return
		}
		isAlias := false
		if match := globalAliasPattern.FindStringSubmatch(key); match != nil {
			isAlias = true
			resolved, err := resolveAlias(config.Storage, match[1])
			if err != nil || resolved == "" {
				noStoreText(w, "Not found", http.StatusNotFound)
				return
			}
			key = resolved
		}
		if !IsPublicObjectKey(key) {
			noStoreText(w, "Not found", http.StatusNotFound)
			return
		}
		rangeHeader := req.Header.Get("Range")
		var (
			object *ObjectBody
			err    error
		)
		if req.Method == http.MethodHead && rangeHeader == "" {
			var stored *StoredObject
			stored, err = config.Storage.Head(req.Context(), key)
			if stored != nil {
				object = &ObjectBody{StoredObject: *stored}
			}
		} else {
			object, err = config.Storage.Get(req.Context(), key, rangeHeader)
		}
		if err != nil {
			if errors.Is(err, ErrRangeNotSatisfiable) {
				noStoreText(w, "Range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			} else {
				noStoreText(w, "Asset storage unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		if object == nil {
			noStoreText(w, "Not found", http.StatusNotFound)
			return
		}
		defer object.Close()
		if access, ok := object.Metadata["raildrop-access"]; ok && access != "public" {
			noStoreText(w, "Not found", http.StatusNotFound)
			return
		}
		expiresAtRaw := object.Metadata["raildrop-expires-at"]
		var expiresAtMs int64
		hasExpiry := expiresAtRaw != ""
		if hasExpiry {
			parsed, parseErr := ParseISOString(expiresAtRaw)
			if parseErr != nil || parsed.UnixMilli() <= time.Now().UnixMilli() {
				noStoreText(w, "Not found", http.StatusNotFound)
				return
			}
			expiresAtMs = parsed.UnixMilli()
		}
		isTemporary := object.Metadata["raildrop-retention"] == "temporary" || strings.HasPrefix(key, "public/tmp/")
		var cacheControl string
		switch {
		case isAlias:
			cacheControl = "public, max-age=300, stale-while-revalidate=86400"
		case isTemporary:
			if hasExpiry {
				maxAge := (expiresAtMs - time.Now().UnixMilli()) / 1000
				if maxAge < 0 {
					maxAge = 0
				}
				cacheControl = "public, max-age=" + strconv.FormatInt(maxAge, 10) + ", must-revalidate"
			} else {
				cacheControl = "no-store"
			}
		case object.CacheControl != nil && *object.CacheControl != "":
			cacheControl = *object.CacheControl
		default:
			cacheControl = "public, max-age=31536000, immutable"
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", cacheControl)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", object.Size))
		contentType := "application/octet-stream"
		if object.ContentType != nil {
			contentType = *object.ContentType
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if object.ETag != nil {
			w.Header().Set("ETag", "\""+*object.ETag+"\"")
		}
		if object.LastModified != nil {
			w.Header().Set("Last-Modified", object.LastModified.UTC().Format(http.TimeFormat))
		}
		if object.ContentDisposition != nil {
			w.Header().Set("Content-Disposition", *object.ContentDisposition)
		} else if unsafeInlineType(object.ContentType) {
			name, ok := metadataDownloadName(object.Metadata)
			if !ok {
				name = "download"
			}
			sanitized := strings.Map(func(character rune) rune {
				if character == '"' || character == '\\' {
					return '_'
				}
				return character
			}, name)
			w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitized+"\"")
		}
		if object.ContentRange != nil {
			w.Header().Set("Content-Range", *object.ContentRange)
		}
		ifNoneMatch := req.Header.Get("If-None-Match")
		etagMatches := false
		if ifNoneMatch != "" && object.ETag != nil {
			for _, candidate := range strings.Split(ifNoneMatch, ",") {
				value := strings.TrimSpace(candidate)
				value = strings.TrimPrefix(value, "W/")
				value = strings.Trim(value, "\"")
				if value == "*" || value == *object.ETag {
					etagMatches = true
					break
				}
			}
		}
		notModifiedSince := false
		if ifModifiedSince := req.Header.Get("If-Modified-Since"); ifModifiedSince != "" && object.LastModified != nil {
			if parsed, parseErr := http.ParseTime(ifModifiedSince); parseErr == nil {
				notModifiedSince = !object.LastModified.UTC().Truncate(time.Second).After(parsed.UTC())
			}
		}
		if ifNoneMatch != "" {
			if etagMatches {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		} else if notModifiedSince {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		status := http.StatusOK
		if rangeHeader != "" {
			status = http.StatusPartialContent
		}
		if req.Method == http.MethodHead {
			w.WriteHeader(status)
			return
		}
		if object.Body != nil {
			w.WriteHeader(status)
			_, _ = io.Copy(w, object.Body)
			return
		}
		w.WriteHeader(status)
	})
}
