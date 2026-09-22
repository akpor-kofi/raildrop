package raildrop

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type ServerUpload struct {
	Body      []byte
	Name      string
	Type      string
	Namespace []string
	Access    Access
	Retention Retention
	ExpiresAt *time.Time
}

type ServerUploadResult struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Access    Access    `json:"access"`
	Retention Retention `json:"retention"`
	PublicURL *string   `json:"publicUrl"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Size      int64     `json:"size"`
	ETag      *string   `json:"etag"`
	Checksum  string    `json:"checksum"`
	ExpiresAt *string   `json:"expiresAt"`
}

type API struct {
	Storage       Storage
	PublicBaseURL string
	Now           func() time.Time
}

func (api *API) now() time.Time {
	if api.Now != nil {
		return api.Now()
	}
	return time.Now()
}

func (api *API) Upload(ctx context.Context, file ServerUpload) (*ServerUploadResult, error) {
	id, err := randomUUID()
	if err != nil {
		return nil, WrapError(CodeStorageError, "Upload id could not be generated.", err)
	}
	access := file.Access
	if access == "" {
		access = AccessPublic
	}
	if access != AccessPublic && access != AccessPrivate {
		return nil, NewError(CodeBadRequest, "Invalid upload access policy.")
	}
	retention := file.Retention
	if retention == "" {
		retention = RetentionPermanent
	}
	if retention != RetentionPermanent && retention != RetentionTemporary {
		return nil, NewError(CodeBadRequest, "Invalid upload retention policy.")
	}
	if retention == RetentionPermanent && file.ExpiresAt != nil {
		return nil, NewError(CodeBadRequest, "Permanent uploads cannot have an expiry.")
	}
	now := api.now()
	expiry := file.ExpiresAt
	if expiry == nil && retention == RetentionTemporary {
		value := now.Add(604_800_000 * time.Millisecond)
		expiry = &value
	}
	if expiry != nil && !expiry.After(now) {
		return nil, NewError(CodeBadRequest, "Temporary upload expiry must be in the future.")
	}
	hash := sha256Hex(file.Body)
	extension := FileExtensionFor(file.Name, "")
	temporaryPrefix := ""
	if retention == RetentionTemporary {
		day := int64(0)
		if expiry != nil {
			day = expiry.UnixMilli() / 86_400_000
		}
		temporaryPrefix = "/tmp/" + strconv.FormatInt(day, 10)
	}
	namespace, err := EncodeNamespace(file.Namespace...)
	if err != nil {
		return nil, err
	}
	key := string(access) + temporaryPrefix + "/" + namespace + "/" + id + "/file." + extension
	metadata := map[string]string{
		"raildrop-id":        id,
		"raildrop-access":    string(access),
		"raildrop-retention": string(retention),
		"raildrop-checksum":  hash,
		"raildrop-name":      Base64EncodeName(file.Name),
	}
	cacheControl := "public, max-age=31536000, immutable"
	if access != AccessPublic {
		cacheControl = "private, no-store"
	}
	if expiry != nil {
		metadata["raildrop-expires-at"] = ISOString(*expiry)
	}
	stored, err := api.Storage.Put(ctx, PutArgs{
		Key:          key,
		Body:         file.Body,
		ContentType:  file.Type,
		CacheControl: cacheControl,
		Metadata:     metadata,
	})
	if err != nil {
		return nil, err
	}
	result := &ServerUploadResult{
		ID:        id,
		Key:       key,
		Access:    access,
		Retention: retention,
		Name:      file.Name,
		Type:      file.Type,
		Size:      int64(len(file.Body)),
		ETag:      stored.ETag,
		Checksum:  hash,
	}
	if access == AccessPublic {
		value := trimTrailingSlash(api.PublicBaseURL) + "/" + key
		result.PublicURL = &value
	}
	if expiry != nil {
		formatted := ISOString(*expiry)
		result.ExpiresAt = &formatted
	}
	return result, nil
}

func (api *API) DeleteFiles(ctx context.Context, keys ...string) error {
	return api.Storage.Delete(ctx, keys...)
}

type SignedURLOptions struct {
	ExpiresIn    int
	DownloadName string
}

func (api *API) GetSignedURL(ctx context.Context, key string, options SignedURLOptions) (string, error) {
	if !strings.HasPrefix(key, "private/") && !strings.HasPrefix(key, "public/") {
		return "", NewError(CodeBadRequest, "Invalid Raildrop object key.")
	}
	expiresIn := options.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 300
	}
	if expiresIn <= 0 || expiresIn > 604_800 {
		return "", NewError(CodeBadRequest, "Signed URL expiry must be between 1 and 604800 seconds.")
	}
	downloadName := ""
	if options.DownloadName != "" {
		downloadName = SanitizeDownloadName(options.DownloadName)
	}
	return api.Storage.SignedURL(ctx, key, expiresIn, downloadName)
}

func (api *API) HeadFile(ctx context.Context, key string) (*StoredObject, error) {
	return api.Storage.Head(ctx, key)
}

func (api *API) CopyFiles(ctx context.Context, sourceKey string, destinationKey string) error {
	return api.Storage.Copy(ctx, sourceKey, destinationKey)
}

func (api *API) ListFiles(ctx context.Context, prefix string, continuationToken string) (*ListResult, error) {
	return api.Storage.List(ctx, prefix, continuationToken)
}
