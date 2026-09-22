package raildrop

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	DefaultUploadExpirySeconds = 60 * 60
	DefaultMultipartThreshold  = 16 * 1_000_000
	DefaultMultipartPartSize   = 8 * 1_000_000
	MinimumMultipartPartSize   = 5_000_000
	MaximumUploadExpirySeconds = 604_800
	MaximumMultipartParts      = 10_000
)

type HandlerConfig struct {
	Router                  Router
	Storage                 Storage
	Secret                  string
	PublicBaseURL           string
	UploadExpirySeconds     int
	MultipartThresholdBytes int64
	MultipartPartSizeBytes  int64
}

type PreparedPart struct {
	PartNumber int    `json:"partNumber"`
	Start      int64  `json:"start"`
	End        int64  `json:"end"`
	URL        string `json:"url"`
}

type PreparedUpload struct {
	Kind     string            `json:"kind"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	UploadID string            `json:"uploadId,omitempty"`
	Parts    []PreparedPart    `json:"parts,omitempty"`
}

type PreparedFile struct {
	ID           string         `json:"id"`
	Key          string         `json:"key"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Size         int64          `json:"size"`
	Access       Access         `json:"access"`
	Retention    Retention      `json:"retention"`
	ExpiresAt    *string        `json:"expiresAt"`
	Upload       PreparedUpload `json:"upload"`
	SessionToken string         `json:"sessionToken"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := MarshalJSON(body)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, err error) {
	if raildropErr, ok := AsError(err); ok {
		writeJSON(w, HTTPStatus(raildropErr.Code), map[string]any{
			"error": map[string]string{"code": string(raildropErr.Code), "message": raildropErr.Message},
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{"code": string(CodeStorageError), "message": "Raildrop request failed."},
	})
}

func getRoute(router Router, endpoint string) (*Route, error) {
	route, ok := router[endpoint]
	if !ok || route == nil {
		return nil, NewError(CodeNotFound, "Unknown upload endpoint: "+endpoint)
	}
	return route, nil
}

type requestedFileWire struct {
	Name         *string  `json:"name"`
	Size         *float64 `json:"size"`
	Type         *string  `json:"type"`
	LastModified *float64 `json:"lastModified"`
}

func decodeRequestedFiles(raw []json.RawMessage) ([]RequestedFile, bool) {
	files := make([]RequestedFile, 0, len(raw))
	for _, entry := range raw {
		var wire requestedFileWire
		if err := json.Unmarshal(entry, &wire); err != nil {
			return nil, false
		}
		if wire.Name == nil || wire.Size == nil || wire.Type == nil {
			return nil, false
		}
		file := RequestedFile{Name: *wire.Name, Type: *wire.Type}
		if *wire.Size != math.Trunc(*wire.Size) || math.IsNaN(*wire.Size) || math.IsInf(*wire.Size, 0) ||
			*wire.Size > float64(math.MaxInt64) || *wire.Size < math.MinInt64 {
			return nil, false
		}
		file.Size = int64(*wire.Size)
		if wire.LastModified != nil {
			if math.IsNaN(*wire.LastModified) || math.IsInf(*wire.LastModified, 0) {
				return nil, false
			}
			file.LastModified = int64(*wire.LastModified)
		}
		files = append(files, file)
	}
	return files, true
}

func validateFiles(route *Route, files []RequestedFile) error {
	if len(files) == 0 {
		return NewError(CodeBadRequest, "At least one file is required.")
	}
	counts := make(map[string]int)
	for _, file := range files {
		if file.Name == "" || file.Size <= 0 || file.Type == "" {
			return NewError(CodeBadRequest, "File metadata is incomplete.")
		}
		exactRule, exactExists := route.Rules[file.Type]
		groupType := MimeGroupFor(file.Type)
		groupRule, groupExists := route.Rules[string(groupType)]
		exactKey := string(groupType)
		rule := groupRule
		ruleExists := groupExists
		if exactExists {
			exactKey = file.Type
			rule = exactRule
			ruleExists = true
		}
		if !ruleExists {
			return NewError(CodeInvalidType, "File type "+file.Type+" is not allowed.")
		}
		if len(rule.MimeTypes) > 0 {
			allowed := false
			for _, mimeType := range rule.MimeTypes {
				if mimeType == file.Type {
					allowed = true
					break
				}
			}
			if !allowed {
				return NewError(CodeInvalidType, "File type "+file.Type+" is not allowed.")
			}
		}
		limit := rule.MaxFileSizeBytes
		if limit <= 0 && rule.MaxFileSize != "" {
			parsed, err := ParseFileSize(rule.MaxFileSize)
			if err != nil {
				return err
			}
			limit = parsed
		}
		if limit > 0 && file.Size > limit {
			return NewError(CodeTooLarge, file.Name+" exceeds the route size limit.")
		}
		counts[exactKey]++
		maxCount := rule.MaxFileCount
		if maxCount <= 0 {
			maxCount = 1
		}
		if counts[exactKey] > maxCount {
			return NewError(CodeBadRequest, "Too many "+exactKey+" files.")
		}
	}
	return nil
}

func extensionFor(file RequestedFile) string {
	return FileExtensionFor(file.Name, file.Type)
}

type ObjectKeyArgs struct {
	Access    Access
	Retention Retention
	ExpiresAt *time.Time
	Namespace []string
	UploadID  string
	File      RequestedFile
}

func createObjectKey(args ObjectKeyArgs) (string, error) {
	prefix := string(args.Access)
	if args.Retention == RetentionTemporary {
		var day int64
		if args.ExpiresAt != nil {
			day = args.ExpiresAt.UnixMilli() / 86_400_000
		}
		prefix = string(args.Access) + "/tmp/" + strconv.FormatInt(day, 10)
	}
	namespace, err := EncodeNamespace(args.Namespace...)
	if err != nil {
		return "", err
	}
	return prefix + "/" + namespace + "/" + args.UploadID + "/file." + extensionFor(args.File), nil
}

type prepareRequest struct {
	Endpoint string            `json:"endpoint"`
	Input    json.RawMessage   `json:"input"`
	Files    []json.RawMessage `json:"files"`
}

func (config HandlerConfig) uploadExpiry() (int, error) {
	expiry := config.UploadExpirySeconds
	if expiry == 0 {
		expiry = DefaultUploadExpirySeconds
	}
	if expiry <= 0 || expiry > MaximumUploadExpirySeconds {
		return 0, NewError(CodeBadRequest, "Upload URL expiry must be between 1 and 604800 seconds.")
	}
	return expiry, nil
}

func (config HandlerConfig) prepare(w http.ResponseWriter, req *http.Request, body prepareRequest) {
	route, err := getRoute(config.Router, body.Endpoint)
	if err != nil {
		writeError(w, err)
		return
	}
	files, ok := decodeRequestedFiles(body.Files)
	if !ok {
		writeError(w, NewError(CodeBadRequest, "Invalid prepare request."))
		return
	}
	if err := validateFiles(route, files); err != nil {
		writeError(w, err)
		return
	}
	metadata, err := route.Middleware(req.Context(), MiddlewareArgs{
		Request: req,
		Input:   body.Input,
		Files:   files,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	override := PolicyFromMetadata(metadata)
	access := route.Access
	if override.Access != "" {
		access = override.Access
	}
	retention := route.Retention
	if override.Retention != "" {
		retention = override.Retention
	}
	temporaryTTL := route.TemporaryTTLSeconds
	if override.TemporaryTTLSeconds != nil {
		temporaryTTL = *override.TemporaryTTLSeconds
	}
	if temporaryTTL <= 0 {
		writeError(w, NewError(CodeBadRequest, "Temporary retention must be positive."))
		return
	}
	uploadExpiry, err := config.uploadExpiry()
	if err != nil {
		writeError(w, err)
		return
	}
	threshold := config.MultipartThresholdBytes
	if threshold == 0 {
		threshold = DefaultMultipartThreshold
	}
	if threshold <= 0 {
		writeError(w, NewError(CodeBadRequest, "Multipart threshold must be a positive integer."))
		return
	}
	partSize := config.MultipartPartSizeBytes
	if partSize == 0 {
		partSize = DefaultMultipartPartSize
	}
	if partSize < MinimumMultipartPartSize {
		partSize = MinimumMultipartPartSize
	}
	uploadExpiresAt := time.Now().UnixMilli() + int64(uploadExpiry)*1000
	ctx := req.Context()
	prepared := make([]PreparedFile, 0, len(files))
	for _, file := range files {
		id, err := randomUUID()
		if err != nil {
			writeError(w, err)
			return
		}
		var expiresAt *time.Time
		var expiresAtString *string
		if retention == RetentionTemporary {
			value := time.Now().Add(time.Duration(temporaryTTL) * time.Second)
			expiresAt = &value
			formatted := ISOString(value)
			expiresAtString = &formatted
		}
		namespace, err := route.Namespace(NamespaceArgs{Input: body.Input, Metadata: metadata, File: file})
		if err != nil {
			writeError(w, err)
			return
		}
		key, err := createObjectKey(ObjectKeyArgs{
			Access:    access,
			Retention: retention,
			ExpiresAt: expiresAt,
			Namespace: namespace,
			UploadID:  id,
			File:      file,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		objectMetadata := map[string]string{
			"raildrop-id":        id,
			"raildrop-access":    string(access),
			"raildrop-retention": string(retention),
			"raildrop-name":      Base64EncodeName(file.Name),
		}
		if expiresAtString != nil {
			objectMetadata["raildrop-expires-at"] = *expiresAtString
		}
		upload := PreparedUpload{}
		if file.Size >= threshold {
			uploadID, err := config.Storage.CreateMultipart(ctx, MultipartArgs{
				Key:         key,
				ContentType: file.Type,
				Metadata:    objectMetadata,
			})
			if err != nil {
				writeError(w, err)
				return
			}
			upload.Kind = "multipart"
			upload.UploadID = uploadID
			partCount := (file.Size + partSize - 1) / partSize
			if partCount > MaximumMultipartParts {
				_ = config.Storage.AbortMultipart(ctx, key, uploadID)
				writeError(w, NewError(CodeTooLarge, "File requires more than 10000 multipart parts."))
				return
			}
			parts := make([]PreparedPart, int(partCount))
			var failure error
			var waitGroup sync.WaitGroup
			for index := int64(0); index < partCount; index++ {
				waitGroup.Add(1)
				go func(index int64) {
					defer waitGroup.Done()
					if failure != nil {
						return
					}
					start := index * partSize
					end := (index + 1) * partSize
					if end > file.Size {
						end = file.Size
					}
					url, err := config.Storage.PresignPart(ctx, PresignPartArgs{
						Key:        key,
						UploadID:   uploadID,
						PartNumber: int(index + 1),
						ExpiresIn:  uploadExpiry,
					})
					if err != nil {
						failure = err
						return
					}
					parts[index] = PreparedPart{PartNumber: int(index + 1), Start: start, End: end, URL: url}
				}(index)
			}
			waitGroup.Wait()
			if failure != nil {
				_ = config.Storage.AbortMultipart(ctx, key, uploadID)
				writeError(w, failure)
				return
			}
			upload.Parts = parts
		} else {
			url, err := config.Storage.PresignPut(ctx, PresignPutArgs{
				Key:         key,
				ContentType: file.Type,
				Metadata:    objectMetadata,
				ExpiresIn:   uploadExpiry,
			})
			if err != nil {
				writeError(w, err)
				return
			}
			headers := map[string]string{"Content-Type": file.Type}
			for name, value := range objectMetadata {
				headers["x-amz-meta-"+name] = value
			}
			upload.Kind = "put"
			upload.URL = url
			upload.Headers = headers
		}
		token, err := CreateSessionToken(SessionPayload{
			ID:              id,
			Endpoint:        body.Endpoint,
			Key:             key,
			File:            file,
			Access:          access,
			Retention:       retention,
			ExpiresAt:       expiresAtString,
			UploadExpiresAt: uploadExpiresAt,
			Metadata:        metadata,
			Input:           body.Input,
			Multipart:       multipartRef(upload),
		}, config.Secret)
		if err != nil {
			writeError(w, err)
			return
		}
		prepared = append(prepared, PreparedFile{
			ID:           id,
			Key:          key,
			Name:         file.Name,
			Type:         file.Type,
			Size:         file.Size,
			Access:       access,
			Retention:    retention,
			ExpiresAt:    expiresAtString,
			Upload:       upload,
			SessionToken: token,
		})
	}
	writeJSON(w, http.StatusOK, prepared)
}

func multipartRef(upload PreparedUpload) *MultipartSession {
	if upload.Kind != "multipart" {
		return nil
	}
	return &MultipartSession{UploadID: upload.UploadID}
}

type sessionRequestBody struct {
	Endpoint     string          `json:"endpoint"`
	SessionToken string          `json:"sessionToken"`
	Parts        json.RawMessage `json:"parts"`
}

func readSessionRequest(body []byte, action string) (*sessionRequestBody, bool) {
	var wire struct {
		Action       string          `json:"action"`
		Endpoint     json.RawMessage `json:"endpoint"`
		SessionToken json.RawMessage `json:"sessionToken"`
		Parts        json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, false
	}
	if wire.Action != action || len(wire.Endpoint) == 0 || len(wire.SessionToken) == 0 {
		return nil, false
	}
	request := &sessionRequestBody{Parts: wire.Parts}
	if err := json.Unmarshal(wire.Endpoint, &request.Endpoint); err != nil {
		return nil, false
	}
	if err := json.Unmarshal(wire.SessionToken, &request.SessionToken); err != nil {
		return nil, false
	}
	return request, true
}

func decodeCompletedParts(raw json.RawMessage) ([]CompletedPart, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var wire []struct {
		PartNumber *float64 `json:"partNumber"`
		ETag       *string  `json:"etag"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, false
	}
	parts := make([]CompletedPart, 0, len(wire))
	for _, entry := range wire {
		if entry.PartNumber == nil || entry.ETag == nil {
			return nil, false
		}
		parts = append(parts, CompletedPart{
			PartNumber: int(*entry.PartNumber),
			ETag:       *entry.ETag,
		})
	}
	return parts, true
}

func (config HandlerConfig) finalize(w http.ResponseWriter, req *http.Request, body *sessionRequestBody) {
	session, err := ReadSessionToken(body.SessionToken, config.Secret)
	if err != nil {
		writeError(w, err)
		return
	}
	if session.Endpoint != body.Endpoint {
		writeError(w, NewError(CodeForbidden, "Upload session does not match this endpoint."))
		return
	}
	route, err := getRoute(config.Router, body.Endpoint)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx := req.Context()
	if session.Multipart != nil {
		existing, headErr := config.Storage.Head(ctx, session.Key)
		if headErr != nil {
			writeError(w, headErr)
			return
		}
		if existing == nil {
			parts, ok := decodeCompletedParts(body.Parts)
			if !ok || len(parts) == 0 {
				writeError(w, NewError(CodeBadRequest, "Multipart parts are required."))
				return
			}
			if err := config.Storage.CompleteMultipart(ctx, CompleteMultipartArgs{
				Key:      session.Key,
				UploadID: session.Multipart.UploadID,
				Parts:    parts,
			}); err != nil {
				writeError(w, err)
				return
			}
		}
	}
	stored, err := config.Storage.Head(ctx, session.Key)
	if err != nil {
		writeError(w, err)
		return
	}
	if stored == nil {
		writeError(w, NewError(CodeNotFound, "Uploaded object was not found."))
		return
	}
	if stored.Size != session.File.Size ||
		stored.ContentType == nil || *stored.ContentType != session.File.Type ||
		stored.Metadata["raildrop-id"] != session.ID ||
		stored.Metadata["raildrop-access"] != string(session.Access) ||
		stored.Metadata["raildrop-retention"] != string(session.Retention) {
		writeError(w, NewError(CodeBadRequest, "Uploaded object metadata does not match the session."))
		return
	}
	var publicURL *string
	if session.Access == AccessPublic {
		value := trimTrailingSlash(config.PublicBaseURL) + "/" + session.Key
		publicURL = &value
	}
	file := UploadedFileInfo{
		ID:        session.ID,
		Key:       session.Key,
		Access:    session.Access,
		Retention: session.Retention,
		PublicURL: publicURL,
		Name:      session.File.Name,
		Type:      session.File.Type,
		Size:      session.File.Size,
		ETag:      stored.ETag,
		ExpiresAt: session.ExpiresAt,
	}
	if err := route.Validate(ValidateArgs{File: file, Stored: stored, Metadata: session.Metadata}); err != nil {
		writeError(w, err)
		return
	}
	var serverData any
	if route.OnUploadComplete != nil {
		value, err := route.OnUploadComplete(ctx, CompletionArgs{
			Input:    session.Input,
			Metadata: session.Metadata,
			File:     file,
		})
		if err != nil {
			if route.OnUploadError != nil {
				_ = route.OnUploadError(UploadErrorArgs{Error: err, Input: session.Input, Metadata: session.Metadata})
			}
			writeError(w, WrapError(CodeCallbackError, "Upload completed but its callback failed.", err))
			return
		}
		serverData = value
	}
	writeJSON(w, http.StatusOK, UploadedFile{UploadedFileInfo: file, ServerData: serverData})
}

func (config HandlerConfig) abort(w http.ResponseWriter, req *http.Request, body *sessionRequestBody) {
	session, err := ReadSessionToken(body.SessionToken, config.Secret)
	if err != nil {
		writeError(w, err)
		return
	}
	if session.Endpoint != body.Endpoint {
		writeError(w, NewError(CodeForbidden, "Upload session does not match this endpoint."))
		return
	}
	ctx := req.Context()
	if session.Multipart != nil {
		existing, headErr := config.Storage.Head(ctx, session.Key)
		if headErr != nil {
			writeError(w, headErr)
			return
		}
		if existing == nil {
			if err := config.Storage.AbortMultipart(ctx, session.Key, session.Multipart.UploadID); err != nil {
				writeError(w, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func trimTrailingSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}

func NewHandler(config HandlerConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": map[string]string{"code": string(CodeBadRequest)},
			})
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			writeError(w, NewError(CodeBadRequest, "Raildrop request body must be valid JSON."))
			return
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
			if err != nil {
				writeError(w, NewError(CodeBadRequest, "Raildrop request body must be valid JSON."))
			} else {
				writeError(w, NewError(CodeBadRequest, "Invalid Raildrop request."))
			}
			return
		}
		action := ""
		if raw, ok := envelope["action"]; ok {
			_ = json.Unmarshal(raw, &action)
		}
		switch action {
		case "prepare":
			var parsed prepareRequest
			_ = json.Unmarshal(body, &parsed)
			if parsed.Endpoint == "" || parsed.Files == nil {
				writeError(w, NewError(CodeBadRequest, "Invalid prepare request."))
				return
			}
			config.prepare(w, req, parsed)
		case "finalize":
			parsed, ok := readSessionRequest(body, "finalize")
			if !ok {
				writeError(w, NewError(CodeBadRequest, "Invalid finalize request."))
				return
			}
			config.finalize(w, req, parsed)
		case "abort":
			parsed, ok := readSessionRequest(body, "abort")
			if !ok {
				writeError(w, NewError(CodeBadRequest, "Invalid abort request."))
				return
			}
			config.abort(w, req, parsed)
		default:
			writeError(w, NewError(CodeBadRequest, "Unknown Raildrop action."))
		}
	})
}
