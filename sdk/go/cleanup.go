package raildrop

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CleanupResult struct {
	ObjectsDeleted          int `json:"objectsDeleted"`
	MultipartUploadsAborted int `json:"multipartUploadsAborted"`
}

var expiryDayPattern = regexp.MustCompile(`^(?:public|private)/tmp/(\d+)/`)

func CleanupExpiredObjects(ctx context.Context, storage Storage, now time.Time, multipartSafetyMs int64) (*CleanupResult, error) {
	if multipartSafetyMs <= 0 {
		multipartSafetyMs = 24 * 60 * 60 * 1000
	}
	cutoffDay := now.UnixMilli() / 86_400_000
	var expiredKeys []string
	for _, access := range []string{"public", "private"} {
		token := ""
		for {
			page, err := storage.List(ctx, access+"/tmp/", token)
			if err != nil {
				return nil, err
			}
			for _, object := range page.Objects {
				match := expiryDayPattern.FindStringSubmatch(object.Key)
				if match == nil {
					continue
				}
				day, err := strconv.ParseInt(match[1], 10, 64)
				if err != nil || day > cutoffDay {
					continue
				}
				stored, err := storage.Head(ctx, object.Key)
				if err != nil {
					return nil, err
				}
				if stored == nil {
					continue
				}
				exactExpiry, ok := stored.Metadata["raildrop-expires-at"]
				if !ok {
					expiredKeys = append(expiredKeys, object.Key)
					continue
				}
				parsed, err := ParseISOString(exactExpiry)
				if err != nil || parsed.UnixMilli() <= now.UnixMilli() {
					expiredKeys = append(expiredKeys, object.Key)
				}
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
	}
	if len(expiredKeys) > 0 {
		if err := storage.Delete(ctx, expiredKeys...); err != nil {
			return nil, err
		}
	}
	multipartAborted := 0
	for _, access := range []string{"public", "private"} {
		uploads, err := storage.ListMultipart(ctx, access+"/")
		if err != nil {
			return nil, err
		}
		for _, upload := range uploads {
			if upload.Initiated != nil && now.UnixMilli()-upload.Initiated.UnixMilli() < multipartSafetyMs {
				continue
			}
			if err := storage.AbortMultipart(ctx, upload.Key, upload.UploadID); err != nil {
				return nil, err
			}
			multipartAborted++
		}
	}
	return &CleanupResult{ObjectsDeleted: len(expiredKeys), MultipartUploadsAborted: multipartAborted}, nil
}

type TrackedChecker func(ctx context.Context, batch []TrackedCandidate) (map[string]bool, error)

type TrackedCandidate struct {
	ID  string
	Key string
}

type ReconcileOptions struct {
	Now       time.Time
	SafetyMs  int64
	BatchSize int
}

type ReconcileResult struct {
	Scanned int `json:"scanned"`
	Deleted int `json:"deleted"`
}

func ReconcileUntrackedObjects(ctx context.Context, storage Storage, isTracked TrackedChecker, options ReconcileOptions) (*ReconcileResult, error) {
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	safetyMs := options.SafetyMs
	if safetyMs <= 0 {
		safetyMs = 24 * 60 * 60 * 1000
	}
	batchSize := options.BatchSize
	if batchSize < 1 {
		batchSize = 100
	}
	if batchSize > 500 {
		batchSize = 500
	}
	result := &ReconcileResult{}
	for _, access := range []string{"public", "private"} {
		token := ""
		for {
			page, err := storage.List(ctx, access+"/", token)
			if err != nil {
				return nil, err
			}
			var candidates []TrackedCandidate
			for _, object := range page.Objects {
				if strings.HasPrefix(object.Key, "public/global/") {
					continue
				}
				if object.LastModified == nil || now.UnixMilli()-object.LastModified.UnixMilli() < safetyMs {
					continue
				}
				stored, err := storage.Head(ctx, object.Key)
				if err != nil {
					return nil, err
				}
				if stored == nil {
					continue
				}
				id, ok := stored.Metadata["raildrop-id"]
				if ok && id != "" {
					candidates = append(candidates, TrackedCandidate{ID: id, Key: object.Key})
				}
			}
			result.Scanned += len(candidates)
			for start := 0; start < len(candidates); start += batchSize {
				end := start + batchSize
				if end > len(candidates) {
					end = len(candidates)
				}
				batch := candidates[start:end]
				tracked, err := isTracked(ctx, batch)
				if err != nil {
					return nil, err
				}
				var orphans []string
				for _, candidate := range batch {
					if !tracked[candidate.Key] {
						orphans = append(orphans, candidate.Key)
					}
				}
				if len(orphans) > 0 {
					if err := storage.Delete(ctx, orphans...); err != nil {
						return nil, err
					}
					result.Deleted += len(orphans)
				}
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
	}
	return result, nil
}
