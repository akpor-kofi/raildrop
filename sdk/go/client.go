package raildrop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

type ClientConfig struct {
	URL             string
	HTTPClient      *http.Client
	PartConcurrency int
	RetryAttempts   int
	Headers         http.Header
}

type UploadFile struct {
	Name         string
	Body         []byte
	Type         string
	LastModified int64
}

type UploadOptions struct {
	Input            any
	Files            []UploadFile
	Headers          http.Header
	OnUploadBegin    func(fileName string)
	OnUploadProgress func(fileName string, progress int)
}

type Client struct {
	url             string
	client          *http.Client
	partConcurrency int
	retryAttempts   int
	headers         http.Header
}

func NewClient(config ClientConfig) *Client {
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	partConcurrency := config.PartConcurrency
	if partConcurrency <= 0 {
		partConcurrency = 3
	}
	retryAttempts := config.RetryAttempts
	if retryAttempts < 0 {
		retryAttempts = 0
	}
	return &Client{
		url:             config.URL,
		client:          httpClient,
		partConcurrency: partConcurrency,
		retryAttempts:   retryAttempts,
		headers:         config.Headers,
	}
}

func (client *Client) applyHeaders(request *http.Request, extra http.Header, contentType string) {
	for name, values := range client.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	for name, values := range extra {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var validErrorCodes = map[string]bool{
	string(CodeBadRequest):    true,
	string(CodeForbidden):     true,
	string(CodeNotFound):      true,
	string(CodeTooLarge):      true,
	string(CodeInvalidType):   true,
	string(CodeExpired):       true,
	string(CodeStorageError):  true,
	string(CodeCallbackError): true,
}

func readResponse(response *http.Response, target any) error {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return WrapError(CodeStorageError, "Raildrop request failed.", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope errorEnvelope
		_ = json.Unmarshal(body, &envelope)
		code := envelope.Error.Code
		if !validErrorCodes[code] {
			code = string(CodeStorageError)
		}
		message := envelope.Error.Message
		if message == "" {
			message = fmt.Sprintf("Raildrop request failed with %d.", response.StatusCode)
		}
		return NewError(ErrorCode(code), message)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return WrapError(CodeStorageError, "Raildrop response could not be parsed.", err)
	}
	return nil
}

func retryableError(err error) bool {
	if raildropErr, ok := AsError(err); ok {
		return raildropErr.Code == CodeStorageError || raildropErr.Code == CodeCallbackError
	}
	return true
}

func (client *Client) retry(ctx context.Context, callback func() error) error {
	var lastError error
	for attempt := 0; attempt <= client.retryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastError = callback()
		if lastError == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryableError(lastError) {
			return lastError
		}
		if attempt == client.retryAttempts {
			break
		}
		delay := 250 * (1 << attempt)
		if delay > 2000 {
			delay = 2000
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(delay) * time.Millisecond):
		}
	}
	return lastError
}

func (client *Client) post(ctx context.Context, payload any, target any) error {
	body, err := MarshalJSON(payload)
	if err != nil {
		return WrapError(CodeBadRequest, "Raildrop request could not be encoded.", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, bytes.NewReader(body))
	if err != nil {
		return WrapError(CodeBadRequest, "Raildrop request could not be created.", err)
	}
	client.applyHeaders(request, nil, "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return WrapError(CodeStorageError, "Raildrop request failed.", err)
	}
	return readResponse(response, target)
}

func (client *Client) Prepare(ctx context.Context, endpoint string, input any, files []UploadFile) ([]PreparedFile, error) {
	wireFiles := make([]RequestedFile, 0, len(files))
	for _, file := range files {
		wireFiles = append(wireFiles, RequestedFile{
			Name:         file.Name,
			Size:         int64(len(file.Body)),
			Type:         file.Type,
			LastModified: file.LastModified,
		})
	}
	var prepared []PreparedFile
	err := client.retry(ctx, func() error {
		prepared = nil
		return client.post(ctx, map[string]any{
			"action":   "prepare",
			"endpoint": endpoint,
			"input":    input,
			"files":    wireFiles,
		}, &prepared)
	})
	if err != nil {
		return nil, err
	}
	if len(prepared) != len(files) {
		return nil, NewError(CodeBadRequest, "Prepare response did not match the requested files.")
	}
	return prepared, nil
}

func (client *Client) putBytes(ctx context.Context, url string, body []byte, headers map[string]string, contentType string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return WrapError(CodeStorageError, "Railway upload failed.", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return WrapError(CodeStorageError, "Railway upload failed.", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return NewError(CodeStorageError, "Railway upload failed.")
	}
	return nil
}

func (client *Client) uploadPut(ctx context.Context, file UploadFile, prepared PreparedFile, options UploadOptions) ([]CompletedPart, error) {
	if prepared.Upload.Kind == "put" {
		if err := client.retry(ctx, func() error {
			return client.putBytes(ctx, prepared.Upload.URL, file.Body, prepared.Upload.Headers, file.Type)
		}); err != nil {
			return nil, err
		}
		if options.OnUploadProgress != nil {
			options.OnUploadProgress(file.Name, 100)
		}
		return nil, nil
	}
	partsMutex := &sync.Mutex{}
	parts := make([]CompletedPart, 0, len(prepared.Upload.Parts))
	completed := make(map[int]bool)
	var pending []PreparedPart
	for _, part := range prepared.Upload.Parts {
		if !completed[part.PartNumber] {
			pending = append(pending, part)
		}
	}
	cursor := 0
	cursorMutex := &sync.Mutex{}
	workerCount := client.partConcurrency
	if workerCount > len(pending) {
		workerCount = len(pending)
	}
	var failure error
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for {
				cursorMutex.Lock()
				if failure != nil || cursor >= len(pending) {
					cursorMutex.Unlock()
					return
				}
				part := pending[cursor]
				cursor++
				cursorMutex.Unlock()
				var uploaded CompletedPart
				err := client.retry(ctx, func() error {
					start := part.Start
					end := part.End
					if start < 0 || start > int64(len(file.Body)) {
						return NewError(CodeStorageError, "Railway multipart upload failed.")
					}
					if end > int64(len(file.Body)) {
						end = int64(len(file.Body))
					}
					request, err := http.NewRequestWithContext(ctx, http.MethodPut, part.URL, bytes.NewReader(file.Body[int(start):int(end)]))
					if err != nil {
						return NewError(CodeStorageError, "Railway multipart upload failed.")
					}
					response, err := client.client.Do(request)
					if err != nil {
						return NewError(CodeStorageError, "Railway multipart upload failed.")
					}
					defer response.Body.Close()
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
					if response.StatusCode < 200 || response.StatusCode >= 300 {
						return NewError(CodeStorageError, "Railway multipart upload failed.")
					}
					etag := response.Header.Get("ETag")
					if etag == "" {
						return NewError(CodeStorageError, "Multipart response did not include ETag.")
					}
					uploaded = CompletedPart{PartNumber: part.PartNumber, ETag: etag}
					return nil
				})
				if err != nil {
					cursorMutex.Lock()
					if failure == nil {
						failure = err
					}
					cursorMutex.Unlock()
					return
				}
				partsMutex.Lock()
				parts = append(parts, uploaded)
				sort.Slice(parts, func(left int, right int) bool {
					return parts[left].PartNumber < parts[right].PartNumber
				})
				partsMutex.Unlock()
				if options.OnUploadProgress != nil && len(prepared.Upload.Parts) > 0 {
					partsMutex.Lock()
					progress := len(parts) * 100 / len(prepared.Upload.Parts)
					partsMutex.Unlock()
					options.OnUploadProgress(file.Name, progress)
				}
			}
		}()
	}
	waitGroup.Wait()
	if failure != nil {
		return nil, failure
	}
	return parts, nil
}

func (client *Client) Finalize(ctx context.Context, endpoint string, prepared PreparedFile, parts []CompletedPart) (*UploadedFile, error) {
	var wireParts []CompletedPart
	if parts != nil {
		wireParts = parts
	}
	var result UploadedFile
	err := client.retry(ctx, func() error {
		return client.post(ctx, map[string]any{
			"action":       "finalize",
			"endpoint":     endpoint,
			"sessionToken": prepared.SessionToken,
			"parts":        wireParts,
		}, &result)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (client *Client) Abort(ctx context.Context, endpoint string, prepared PreparedFile) error {
	raw := json.RawMessage(nil)
	return client.post(ctx, map[string]any{
		"action":       "abort",
		"endpoint":     endpoint,
		"sessionToken": prepared.SessionToken,
	}, &raw)
}

func (client *Client) Upload(ctx context.Context, endpoint string, options UploadOptions) ([]UploadedFile, error) {
	if len(options.Files) == 0 {
		return nil, NewError(CodeBadRequest, "At least one file is required.")
	}
	prepared, err := client.Prepare(ctx, endpoint, options.Input, options.Files)
	if err != nil {
		return nil, err
	}
	results := make([]UploadedFile, 0, len(options.Files))
	for index, file := range options.Files {
		entry := prepared[index]
		if options.OnUploadBegin != nil {
			options.OnUploadBegin(file.Name)
		}
		parts, err := client.uploadPut(ctx, file, entry, options)
		if err != nil {
			return nil, err
		}
		uploaded, err := client.Finalize(ctx, endpoint, entry, parts)
		if err != nil {
			return nil, err
		}
		results = append(results, *uploaded)
	}
	return results, nil
}
