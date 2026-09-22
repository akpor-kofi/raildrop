package s3store

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
)

func md5Digest(payload []byte) []byte {
	sum := md5.Sum(payload)
	return sum[:]
}

type RailwayBucketStorage struct {
	config raildrop.BucketConfig
	client *http.Client
	now    func() time.Time
	scheme string
	host   string
	prefix string
}

func NewRailwayBucketStorage(config raildrop.BucketConfig, client *http.Client) (*RailwayBucketStorage, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("s3store: invalid bucket endpoint %q", config.Endpoint)
	}
	region := config.Region
	if region == "" {
		region = "auto"
	}
	resolved := config
	resolved.Region = region
	return &RailwayBucketStorage{
		config: resolved,
		client: client,
		now:    time.Now,
		scheme: parsed.Scheme,
		host:   parsed.Host,
		prefix: strings.TrimSuffix(parsed.Path, "/"),
	}, nil
}

func BucketConfigFromEnv() (raildrop.BucketConfig, error) {
	required := func(raildropName string, railwayName string) (string, error) {
		value := os.Getenv(raildropName)
		if value == "" {
			value = os.Getenv(railwayName)
		}
		if value == "" {
			return "", fmt.Errorf("s3store: missing %s (or Railway %s)", raildropName, railwayName)
		}
		return value, nil
	}
	bucket, err := required("RAILDROP_BUCKET", "BUCKET")
	if err != nil {
		return raildrop.BucketConfig{}, err
	}
	endpoint, err := required("RAILDROP_ENDPOINT", "ENDPOINT")
	if err != nil {
		return raildrop.BucketConfig{}, err
	}
	accessKeyID, err := required("RAILDROP_ACCESS_KEY_ID", "ACCESS_KEY_ID")
	if err != nil {
		return raildrop.BucketConfig{}, err
	}
	secretAccessKey, err := required("RAILDROP_SECRET_ACCESS_KEY", "SECRET_ACCESS_KEY")
	if err != nil {
		return raildrop.BucketConfig{}, err
	}
	region := os.Getenv("RAILDROP_REGION")
	if region == "" {
		region = os.Getenv("REGION")
	}
	if region == "" {
		region = "auto"
	}
	return raildrop.BucketConfig{
		Bucket:          bucket,
		Endpoint:        endpoint,
		Region:          region,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		ForcePathStyle:  os.Getenv("RAILDROP_FORCE_PATH_STYLE") == "true",
	}, nil
}

func (storage *RailwayBucketStorage) requestHost() string {
	if storage.config.ForcePathStyle {
		return storage.host
	}
	return storage.config.Bucket + "." + storage.host
}

func (storage *RailwayBucketStorage) requestPath(key string) string {
	encoded := encodeKeyPath(key)
	if storage.config.ForcePathStyle {
		return storage.prefix + "/" + storage.config.Bucket + "/" + encoded
	}
	return storage.prefix + "/" + encoded
}

func (storage *RailwayBucketStorage) rootPath() string {
	if storage.config.ForcePathStyle {
		return storage.prefix + "/" + storage.config.Bucket
	}
	if storage.prefix == "" {
		return "/"
	}
	return storage.prefix
}

func metadataHeaders(metadata map[string]string) map[string]string {
	headers := make(map[string]string, len(metadata))
	for name, value := range metadata {
		headers["x-amz-meta-"+name] = value
	}
	return headers
}

func (storage *RailwayBucketStorage) do(ctx context.Context, method string, path string, query url.Values, headers map[string]string, body []byte) (*http.Response, error) {
	payloadHash := sha256HexBytes(body)
	sign := signRequest{
		Method:          method,
		Host:            storage.requestHost(),
		Path:            path,
		Query:           query,
		Headers:         headers,
		PayloadHash:     payloadHash,
		Region:          storage.config.Region,
		AccessKeyID:     storage.config.AccessKeyID,
		SecretAccessKey: storage.config.SecretAccessKey,
		Now:             storage.now(),
	}
	authorize(&sign)
	target := storage.scheme + "://" + storage.requestHost() + path
	if encodedQuery := canonicalQuery(query); encodedQuery != "" {
		target += "?" + encodedQuery
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("s3store: request could not be created: %w", err)
	}
	request.Header.Set("x-amz-date", sign.AmzDate)
	request.Header.Set("x-amz-content-sha256", payloadHash)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("Authorization", sign.Authorization)
	client := storage.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("s3store: request failed: %w", err)
	}
	return response, nil
}

func storageError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	var parsed errorResponseXML
	if err := parseXML(body, &parsed); err == nil && parsed.Code != "" {
		return fmt.Errorf("s3store: storage request failed (%s): %s", parsed.Code, parsed.Message)
	}
	return fmt.Errorf("s3store: storage request failed with status %d", response.StatusCode)
}

func metadataFromHeaders(response *http.Response) map[string]string {
	metadata := make(map[string]string)
	for name, values := range response.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-meta-") && len(values) > 0 {
			metadata[strings.TrimPrefix(lower, "x-amz-meta-")] = values[0]
		}
	}
	return metadata
}

func headerValue(response *http.Response, name string) *string {
	value := response.Header.Get(name)
	if value == "" {
		return nil
	}
	return &value
}

func storedObjectFromResponse(key string, response *http.Response) *raildrop.StoredObject {
	var lastModified *time.Time
	if raw := headerValue(response, "Last-Modified"); raw != nil {
		if parsed, err := http.ParseTime(*raw); err == nil {
			lastModified = &parsed
		}
	}
	var etag *string
	if raw := headerValue(response, "ETag"); raw != nil {
		trimmed := trimETag(*raw)
		etag = &trimmed
	}
	var size int64
	if raw := headerValue(response, "Content-Length"); raw != nil {
		fmt.Sscanf(*raw, "%d", &size)
	}
	return &raildrop.StoredObject{
		Key:                key,
		Size:               size,
		ContentType:        headerValue(response, "Content-Type"),
		ETag:               etag,
		LastModified:       lastModified,
		Metadata:           metadataFromHeaders(response),
		ContentDisposition: headerValue(response, "Content-Disposition"),
		CacheControl:       headerValue(response, "Cache-Control"),
	}
}

func (storage *RailwayBucketStorage) PresignPut(ctx context.Context, args raildrop.PresignPutArgs) (string, error) {
	headers := metadataHeaders(args.Metadata)
	if args.ContentType != "" {
		headers["content-type"] = args.ContentType
	}
	return presign(presignArgs{
		Method:          http.MethodPut,
		Scheme:          storage.scheme,
		Host:            storage.requestHost(),
		Path:            storage.requestPath(args.Key),
		Query:           url.Values{},
		Headers:         headers,
		Region:          storage.config.Region,
		AccessKeyID:     storage.config.AccessKeyID,
		SecretAccessKey: storage.config.SecretAccessKey,
		ExpiresIn:       args.ExpiresIn,
		Now:             storage.now(),
	}), nil
}

func (storage *RailwayBucketStorage) CreateMultipart(ctx context.Context, args raildrop.MultipartArgs) (string, error) {
	headers := metadataHeaders(args.Metadata)
	if args.ContentType != "" {
		headers["content-type"] = args.ContentType
	}
	query := url.Values{"uploads": {""}}
	response, err := storage.do(ctx, http.MethodPost, storage.requestPath(args.Key), query, headers, nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return "", storageError(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("s3store: multipart response could not be read: %w", err)
	}
	var parsed initiateMultipartUploadResultXML
	if err := parseXML(body, &parsed); err != nil || parsed.UploadID == "" {
		return "", fmt.Errorf("s3store: storage did not return a multipart upload id")
	}
	return parsed.UploadID, nil
}

func (storage *RailwayBucketStorage) PresignPart(ctx context.Context, args raildrop.PresignPartArgs) (string, error) {
	query := url.Values{
		"partNumber": {fmt.Sprintf("%d", args.PartNumber)},
		"uploadId":   {args.UploadID},
	}
	return presign(presignArgs{
		Method:          http.MethodPut,
		Scheme:          storage.scheme,
		Host:            storage.requestHost(),
		Path:            storage.requestPath(args.Key),
		Query:           query,
		Headers:         map[string]string{},
		Region:          storage.config.Region,
		AccessKeyID:     storage.config.AccessKeyID,
		SecretAccessKey: storage.config.SecretAccessKey,
		ExpiresIn:       args.ExpiresIn,
		Now:             storage.now(),
	}), nil
}

func (storage *RailwayBucketStorage) CompleteMultipart(ctx context.Context, args raildrop.CompleteMultipartArgs) error {
	parts := make([]completePartXML, 0, len(args.Parts))
	for _, part := range args.Parts {
		parts = append(parts, completePartXML{PartNumber: part.PartNumber, ETag: trimETag(part.ETag)})
	}
	payload, err := xml.Marshal(completeMultipartUploadXML{Parts: parts})
	if err != nil {
		return fmt.Errorf("s3store: multipart completion could not be encoded: %w", err)
	}
	query := url.Values{"uploadId": {args.UploadID}}
	response, err := storage.do(ctx, http.MethodPost, storage.requestPath(args.Key), query, map[string]string{"content-type": "application/xml"}, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return storageError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return nil
}

func (storage *RailwayBucketStorage) AbortMultipart(ctx context.Context, key string, uploadID string) error {
	query := url.Values{"uploadId": {uploadID}}
	response, err := storage.do(ctx, http.MethodDelete, storage.requestPath(key), query, nil, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return storageError(response)
	}
	return nil
}

func (storage *RailwayBucketStorage) Head(ctx context.Context, key string) (*raildrop.StoredObject, error) {
	response, err := storage.do(ctx, http.MethodHead, storage.requestPath(key), url.Values{}, nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode >= 400 {
		return nil, storageError(response)
	}
	return storedObjectFromResponse(key, response), nil
}

func (storage *RailwayBucketStorage) Get(ctx context.Context, key string, rangeHeader string) (*raildrop.ObjectBody, error) {
	var headers map[string]string
	if rangeHeader != "" {
		headers = map[string]string{"range": rangeHeader}
	}
	response, err := storage.do(ctx, http.MethodGet, storage.requestPath(key), url.Values{}, headers, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		_ = response.Body.Close()
		return nil, nil
	}
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		_ = response.Body.Close()
		return nil, raildrop.ErrRangeNotSatisfiable
	}
	if response.StatusCode >= 400 {
		defer func() { _ = response.Body.Close() }()
		return nil, storageError(response)
	}
	stored := storedObjectFromResponse(key, response)
	var contentRange *string
	if value := response.Header.Get("Content-Range"); value != "" {
		contentRange = &value
	}
	return &raildrop.ObjectBody{
		StoredObject: *stored,
		Body:         response.Body,
		ContentRange: contentRange,
	}, nil
}

func (storage *RailwayBucketStorage) Put(ctx context.Context, args raildrop.PutArgs) (*raildrop.StoredObject, error) {
	headers := metadataHeaders(args.Metadata)
	if args.ContentType != "" {
		headers["content-type"] = args.ContentType
	}
	if args.CacheControl != "" {
		headers["cache-control"] = args.CacheControl
	}
	response, err := storage.do(ctx, http.MethodPut, storage.requestPath(args.Key), url.Values{}, headers, args.Body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 400 {
		return nil, storageError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	stored, err := storage.Head(ctx, args.Key)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("s3store: uploaded object could not be verified")
	}
	return stored, nil
}

func (storage *RailwayBucketStorage) Delete(ctx context.Context, keys ...string) error {
	for start := 0; start < len(keys); start += 1000 {
		end := start + 1000
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		objects := make([]deleteKeyXML, 0, len(chunk))
		for _, key := range chunk {
			objects = append(objects, deleteKeyXML{Key: key})
		}
		payload, err := xml.Marshal(deleteObjectsXML{Quiet: true, Objects: objects})
		if err != nil {
			return fmt.Errorf("s3store: delete batch could not be encoded: %w", err)
		}
		contentMD5 := base64.StdEncoding.EncodeToString(md5Digest(payload))
		query := url.Values{"delete": {""}}
		response, err := storage.do(ctx, http.MethodPost, storage.rootPath(), query, map[string]string{"content-type": "application/xml", "content-md5": contentMD5}, payload)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		_ = response.Body.Close()
		if response.StatusCode >= 400 {
			return fmt.Errorf("s3store: storage request failed with status %d", response.StatusCode)
		}
		var parsed deleteResultXML
		if err := parseXML(body, &parsed); err == nil && len(parsed.Errors) > 0 {
			return fmt.Errorf("s3store: storage failed to delete %d object(s)", len(parsed.Errors))
		}
	}
	return nil
}

func (storage *RailwayBucketStorage) Copy(ctx context.Context, sourceKey string, destinationKey string) error {
	source := storage.config.Bucket + "/" + encodeKeyPath(sourceKey)
	response, err := storage.do(ctx, http.MethodPut, storage.requestPath(destinationKey), url.Values{}, map[string]string{"x-amz-copy-source": source}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 400 {
		return storageError(response)
	}
	return nil
}

func (storage *RailwayBucketStorage) List(ctx context.Context, prefix string, continuationToken string) (*raildrop.ListResult, error) {
	result := &raildrop.ListResult{Objects: []raildrop.StoredObject{}}
	query := url.Values{
		"list-type": {"2"},
		"prefix":    {prefix},
	}
	if continuationToken != "" {
		query.Set("continuation-token", continuationToken)
	}
	response, err := storage.do(ctx, http.MethodGet, storage.rootPath(), query, nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 400 {
		return nil, storageError(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("s3store: list response could not be read: %w", err)
	}
	var parsed listBucketResultXML
	if err := parseXML(body, &parsed); err != nil {
		return nil, fmt.Errorf("s3store: list response could not be parsed: %w", err)
	}
	for _, entry := range parsed.Contents {
		etag := trimETag(entry.ETag)
		lastModified := entry.LastModified.Time
		result.Objects = append(result.Objects, raildrop.StoredObject{
			Key:          entry.Key,
			Size:         entry.Size,
			ETag:         &etag,
			LastModified: &lastModified,
			Metadata:     map[string]string{},
		})
	}
	result.NextToken = parsed.NextContinuationToken
	return result, nil
}

func (storage *RailwayBucketStorage) ListMultipart(ctx context.Context, prefix string) ([]raildrop.MultipartUpload, error) {
	uploads := []raildrop.MultipartUpload{}
	keyMarker := ""
	uploadIDMarker := ""
	for {
		query := url.Values{"uploads": {""}}
		if prefix != "" {
			query.Set("prefix", prefix)
		}
		if keyMarker != "" {
			query.Set("key-marker", keyMarker)
		}
		if uploadIDMarker != "" {
			query.Set("upload-id-marker", uploadIDMarker)
		}
		response, err := storage.do(ctx, http.MethodGet, storage.rootPath(), query, nil, nil)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		_ = response.Body.Close()
		if response.StatusCode >= 400 {
			return nil, storageError(response)
		}
		if readErr != nil {
			return nil, fmt.Errorf("s3store: multipart listing could not be read: %w", readErr)
		}
		var parsed listMultipartUploadsResultXML
		if err := parseXML(body, &parsed); err != nil {
			return nil, fmt.Errorf("s3store: multipart listing could not be parsed: %w", err)
		}
		for _, upload := range parsed.Uploads {
			initiated := upload.Initiated.Time
			uploads = append(uploads, raildrop.MultipartUpload{
				Key:       upload.Key,
				UploadID:  upload.UploadID,
				Initiated: &initiated,
			})
		}
		if !parsed.IsTruncated {
			return uploads, nil
		}
		keyMarker = parsed.NextKeyMarker
		uploadIDMarker = parsed.NextUploadIDMarker
		if keyMarker == "" && uploadIDMarker == "" {
			return nil, fmt.Errorf("s3store: storage returned a truncated multipart listing without continuation")
		}
	}
}

func (storage *RailwayBucketStorage) SignedURL(ctx context.Context, key string, expiresIn int, downloadName string) (string, error) {
	query := url.Values{}
	if downloadName != "" {
		query.Set("response-content-disposition", fmt.Sprintf("attachment; filename=\"%s\"", sanitizeDispositionName(downloadName)))
	}
	return presign(presignArgs{
		Method:          http.MethodGet,
		Scheme:          storage.scheme,
		Host:            storage.requestHost(),
		Path:            storage.requestPath(key),
		Query:           query,
		Headers:         map[string]string{},
		Region:          storage.config.Region,
		AccessKeyID:     storage.config.AccessKeyID,
		SecretAccessKey: storage.config.SecretAccessKey,
		ExpiresIn:       expiresIn,
		Now:             storage.now(),
	}), nil
}

func sanitizeDispositionName(name string) string {
	replaced := strings.Map(func(character rune) rune {
		if character == '"' || character == '\\' {
			return '_'
		}
		return character
	}, name)
	return replaced
}

func (storage *RailwayBucketStorage) GetCORSRules(ctx context.Context) ([]raildrop.CORSRule, error) {
	query := url.Values{"cors": {""}}
	response, err := storage.do(ctx, http.MethodGet, storage.rootPath(), query, nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return []raildrop.CORSRule{}, nil
	}
	if response.StatusCode >= 400 {
		return nil, storageError(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("s3store: cors response could not be read: %w", err)
	}
	var parsed corsConfigurationXML
	if err := parseXML(body, &parsed); err != nil {
		return nil, fmt.Errorf("s3store: cors response could not be parsed: %w", err)
	}
	rules := make([]raildrop.CORSRule, 0, len(parsed.Rules))
	for _, rule := range parsed.Rules {
		rules = append(rules, raildrop.CORSRule{
			AllowedOrigins: rule.AllowedOrigin,
			AllowedMethods: rule.AllowedMethod,
			AllowedHeaders: rule.AllowedHeader,
			ExposeHeaders:  rule.ExposeHeader,
			MaxAgeSeconds:  rule.MaxAgeSeconds,
		})
	}
	return rules, nil
}

func (storage *RailwayBucketStorage) SetCORSRules(ctx context.Context, origins []string) error {
	configuration := corsConfigurationXML{
		Rules: []corsRuleXML{
			{
				AllowedOrigin: origins,
				AllowedMethod: []string{"PUT", "GET", "HEAD"},
				AllowedHeader: []string{"*"},
				ExposeHeader:  []string{"etag"},
				MaxAgeSeconds: 600,
			},
		},
	}
	payload, err := xml.Marshal(configuration)
	if err != nil {
		return fmt.Errorf("s3store: cors configuration could not be encoded: %w", err)
	}
	query := url.Values{"cors": {""}}
	response, err := storage.do(ctx, http.MethodPut, storage.rootPath(), query, map[string]string{"content-type": "application/xml"}, payload)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 400 {
		return storageError(response)
	}
	return nil
}
