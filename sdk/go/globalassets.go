package raildrop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

type GlobalAssetSource struct {
	Alias       string
	Body        []byte
	Filename    string
	ContentType string
	Namespace   []string
}

type GlobalAssetManifestEntry struct {
	Key         string `json:"key"`
	Checksum    string `json:"checksum"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Filename    string `json:"filename"`
}

type GlobalAssetManifest struct {
	Version     int                                 `json:"version"`
	GeneratedAt string                              `json:"generatedAt"`
	Assets      map[string]GlobalAssetManifestEntry `json:"assets"`
}

type GlobalAssetSyncResult struct {
	GlobalAssetManifest
	OrphanedKeys []string `json:"orphanedKeys"`
}

type GlobalAssetMirrorTarget struct {
	Name    string
	Storage Storage
}

type GlobalAssetMirrorTargetResult struct {
	Copied   int `json:"copied"`
	Verified int `json:"verified"`
}

type GlobalAssetMirrorResult struct {
	Targets            map[string]GlobalAssetMirrorTargetResult `json:"targets"`
	ObjectCount        int                                      `json:"objectCount"`
	ManifestAssetCount int                                      `json:"manifestAssetCount"`
}

var globalAssetAliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,119}$`)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func readManifestBody(storage Storage, ctx context.Context) ([]byte, bool, error) {
	object, err := storage.Get(ctx, GlobalManifestKey, "")
	if err != nil {
		return nil, false, err
	}
	if object == nil {
		return nil, false, nil
	}
	defer object.Close()
	body, err := io.ReadAll(io.LimitReader(object.Body, 32<<20))
	if err != nil {
		return nil, false, fmt.Errorf("global asset manifest could not be read: %w", err)
	}
	return body, true, nil
}

func parseGlobalManifest(ctx context.Context, storage Storage) (*GlobalAssetManifest, error) {
	body, ok, err := readManifestBody(storage, ctx)
	if err != nil || !ok {
		return nil, err
	}
	var parsed struct {
		Version     *int                                `json:"version"`
		GeneratedAt *string                             `json:"generatedAt"`
		Assets      map[string]GlobalAssetManifestEntry `json:"assets"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid global asset manifest encountered")
	}
	if parsed.Version == nil || *parsed.Version != 1 || parsed.GeneratedAt == nil || parsed.Assets == nil {
		return nil, fmt.Errorf("invalid global asset manifest encountered")
	}
	for alias, entry := range parsed.Assets {
		if !globalAssetAliasPattern.MatchString(alias) || !isValidManifestEntry(alias, entry) {
			return nil, fmt.Errorf("invalid global asset manifest encountered")
		}
	}
	return &GlobalAssetManifest{
		Version:     *parsed.Version,
		GeneratedAt: *parsed.GeneratedAt,
		Assets:      parsed.Assets,
	}, nil
}

func isValidManifestEntry(alias string, entry GlobalAssetManifestEntry) bool {
	if !strings.HasPrefix(entry.Key, "public/global/") || entry.Key == GlobalManifestKey {
		return false
	}
	if !sha256Pattern.MatchString(entry.Checksum) {
		return false
	}
	if !strings.Contains(entry.Key, "/"+entry.Checksum+"/file.") {
		return false
	}
	if entry.Size < 0 || entry.ContentType == "" || entry.Filename == "" {
		return false
	}
	return true
}

func writeGlobalAssetEntries(ctx context.Context, storage Storage, sources []GlobalAssetSource) (map[string]GlobalAssetManifestEntry, error) {
	assets := make(map[string]GlobalAssetManifestEntry)
	seen := make(map[string]bool)
	for _, source := range sources {
		if !globalAssetAliasPattern.MatchString(source.Alias) {
			return nil, fmt.Errorf("invalid global asset alias: %s", source.Alias)
		}
		if seen[source.Alias] {
			return nil, fmt.Errorf("duplicate global asset alias: %s", source.Alias)
		}
		seen[source.Alias] = true
		if _, err := Namespace(source.Namespace...); err != nil {
			return nil, err
		}
		checksum := sha256Hex(source.Body)
		extension := FileExtensionFor(source.Filename, "")
		namespace, err := EncodeNamespace(source.Namespace...)
		if err != nil {
			return nil, err
		}
		key := "public/global/" + namespace + "/" + checksum + "/file." + extension
		existing, err := storage.Head(ctx, key)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			if _, err := storage.Put(ctx, PutArgs{
				Key:          key,
				Body:         source.Body,
				ContentType:  source.ContentType,
				CacheControl: "public, max-age=31536000, immutable",
				Metadata: map[string]string{
					"raildrop-access":    "public",
					"raildrop-retention": "permanent",
					"raildrop-checksum":  checksum,
				},
			}); err != nil {
				return nil, err
			}
		}
		verified, err := storage.Head(ctx, key)
		if err != nil {
			return nil, err
		}
		verifiedBody, err := storage.Get(ctx, key, "")
		if err != nil {
			return nil, err
		}
		verifiedChecksum := ""
		if verifiedBody != nil {
			verifiedBytes, err := io.ReadAll(verifiedBody.Body)
			closeErr := verifiedBody.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			verifiedChecksum = sha256Hex(verifiedBytes)
		}
		storedChecksum := ""
		if verified != nil {
			storedChecksum = verified.Metadata["raildrop-checksum"]
		}
		if verified == nil ||
			verified.Size != int64(len(source.Body)) ||
			verified.ContentType == nil || *verified.ContentType != source.ContentType ||
			storedChecksum != checksum ||
			verifiedChecksum != checksum {
			return nil, fmt.Errorf("global asset verification failed for alias %s", source.Alias)
		}
		assets[source.Alias] = GlobalAssetManifestEntry{
			Key:         key,
			Checksum:    checksum,
			Size:        int64(len(source.Body)),
			ContentType: source.ContentType,
			Filename:    source.Filename,
		}
	}
	return assets, nil
}

func writeGlobalManifest(ctx context.Context, storage Storage, assets map[string]GlobalAssetManifestEntry) (*GlobalAssetManifest, error) {
	manifest := &GlobalAssetManifest{
		Version:     1,
		GeneratedAt: ISOString(time.Now()),
		Assets:      assets,
	}
	payload, err := MarshalJSON(manifest)
	if err != nil {
		return nil, err
	}
	if _, err := storage.Put(ctx, PutArgs{
		Key:          GlobalManifestKey,
		Body:         payload,
		ContentType:  "application/json",
		CacheControl: "public, max-age=300, stale-while-revalidate=86400",
		Metadata: map[string]string{
			"raildrop-access":    "public",
			"raildrop-retention": "permanent",
		},
	}); err != nil {
		return nil, err
	}
	return manifest, nil
}

func findOrphanedKeys(previous map[string]GlobalAssetManifestEntry, active map[string]GlobalAssetManifestEntry) []string {
	activeKeys := make(map[string]bool)
	for _, asset := range active {
		activeKeys[asset.Key] = true
	}
	previousKeys := make(map[string]bool)
	for _, asset := range previous {
		previousKeys[asset.Key] = true
	}
	var orphaned []string
	for key := range previousKeys {
		if !activeKeys[key] {
			orphaned = append(orphaned, key)
		}
	}
	return orphaned
}

func SyncGlobalAssets(ctx context.Context, storage Storage, sources []GlobalAssetSource) (*GlobalAssetSyncResult, error) {
	previousBody, ok, err := readManifestBody(storage, ctx)
	if err != nil {
		return nil, err
	}
	var previousAssets map[string]GlobalAssetManifestEntry
	if ok {
		var lenient struct {
			Assets map[string]GlobalAssetManifestEntry `json:"assets"`
		}
		if json.Unmarshal(previousBody, &lenient) == nil {
			previousAssets = lenient.Assets
		}
	}
	assets, err := writeGlobalAssetEntries(ctx, storage, sources)
	if err != nil {
		return nil, err
	}
	manifest, err := writeGlobalManifest(ctx, storage, assets)
	if err != nil {
		return nil, err
	}
	if previousAssets == nil {
		previousAssets = map[string]GlobalAssetManifestEntry{}
	}
	return &GlobalAssetSyncResult{GlobalAssetManifest: *manifest, OrphanedKeys: findOrphanedKeys(previousAssets, assets)}, nil
}

func UpsertGlobalAssets(ctx context.Context, storage Storage, sources []GlobalAssetSource) (*GlobalAssetSyncResult, error) {
	previousManifest, err := parseGlobalManifest(ctx, storage)
	if err != nil {
		return nil, err
	}
	upserted, err := writeGlobalAssetEntries(ctx, storage, sources)
	if err != nil {
		return nil, err
	}
	assets := make(map[string]GlobalAssetManifestEntry)
	if previousManifest != nil {
		for alias, entry := range previousManifest.Assets {
			assets[alias] = entry
		}
	}
	for alias, entry := range upserted {
		assets[alias] = entry
	}
	manifest, err := writeGlobalManifest(ctx, storage, assets)
	if err != nil {
		return nil, err
	}
	previous := map[string]GlobalAssetManifestEntry{}
	if previousManifest != nil {
		previous = previousManifest.Assets
	}
	if previous == nil {
		previous = map[string]GlobalAssetManifestEntry{}
	}
	return &GlobalAssetSyncResult{GlobalAssetManifest: *manifest, OrphanedKeys: findOrphanedKeys(previous, assets)}, nil
}

func listAllGlobalObjects(ctx context.Context, storage Storage) ([]StoredObject, error) {
	var objects []StoredObject
	token := ""
	for {
		page, err := storage.List(ctx, "public/global/", token)
		if err != nil {
			return nil, err
		}
		objects = append(objects, page.Objects...)
		if page.NextToken == "" {
			return objects, nil
		}
		token = page.NextToken
	}
}

func readObjectBytes(ctx context.Context, storage Storage, key string) ([]byte, error) {
	object, err := storage.Get(ctx, key, "")
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("global asset disappeared while mirroring: %s", key)
	}
	defer object.Close()
	return io.ReadAll(object.Body)
}

func MirrorGlobalAssets(ctx context.Context, targets []GlobalAssetMirrorTarget) (*GlobalAssetMirrorResult, error) {
	if len(targets) < 2 {
		return nil, fmt.Errorf("at least two Raildrop mirror targets are required")
	}
	type inventory struct {
		target   GlobalAssetMirrorTarget
		objects  []StoredObject
		manifest *GlobalAssetManifest
	}
	inventories := make([]inventory, 0, len(targets))
	for _, target := range targets {
		objects, err := listAllGlobalObjects(ctx, target.Storage)
		if err != nil {
			return nil, err
		}
		manifest, err := parseGlobalManifest(ctx, target.Storage)
		if err != nil {
			return nil, err
		}
		inventories = append(inventories, inventory{target: target, objects: objects, manifest: manifest})
	}
	objectSources := make(map[string]GlobalAssetMirrorTarget)
	objectSizes := make(map[string]int64)
	for _, entry := range inventories {
		for _, object := range entry.objects {
			if object.Key == GlobalManifestKey {
				continue
			}
			if _, ok := objectSources[object.Key]; ok {
				if objectSizes[object.Key] != object.Size {
					return nil, fmt.Errorf("global asset conflict for %s; refusing to overwrite it", object.Key)
				}
				continue
			}
			objectSources[object.Key] = entry.target
			objectSizes[object.Key] = object.Size
		}
	}
	type canonicalObject struct {
		body         []byte
		checksum     string
		contentType  string
		cacheControl string
		metadata     map[string]string
	}
	canonicalObjects := make(map[string]canonicalObject)
	for key, source := range objectSources {
		body, err := readObjectBytes(ctx, source.Storage, key)
		if err != nil {
			return nil, err
		}
		stored, err := source.Storage.Head(ctx, key)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, fmt.Errorf("global asset disappeared while mirroring: %s", key)
		}
		contentType := "application/octet-stream"
		if stored.ContentType != nil {
			contentType = *stored.ContentType
		}
		cacheControl := ""
		if stored.CacheControl != nil {
			cacheControl = *stored.CacheControl
		}
		canonicalObjects[key] = canonicalObject{
			body:         body,
			checksum:     sha256Hex(body),
			contentType:  contentType,
			cacheControl: cacheControl,
			metadata:     stored.Metadata,
		}
	}
	assets := make(map[string]GlobalAssetManifestEntry)
	for _, entry := range inventories {
		if entry.manifest == nil {
			continue
		}
		for alias, manifestEntry := range entry.manifest.Assets {
			if existing, ok := assets[alias]; ok {
				if existing.Key != manifestEntry.Key || existing.Checksum != manifestEntry.Checksum {
					return nil, fmt.Errorf("global asset alias conflict for %s; refusing to merge manifests", alias)
				}
				continue
			}
			assets[alias] = manifestEntry
		}
	}
	for alias, entry := range assets {
		object, ok := canonicalObjects[entry.Key]
		if !ok {
			return nil, fmt.Errorf("manifest entry %s has no available global object", alias)
		}
		if object.checksum != entry.Checksum || int64(len(object.body)) != entry.Size {
			return nil, fmt.Errorf("manifest entry %s does not match an available global object", alias)
		}
	}
	result := &GlobalAssetMirrorResult{
		Targets:            make(map[string]GlobalAssetMirrorTargetResult),
		ObjectCount:        len(canonicalObjects) + 1,
		ManifestAssetCount: len(assets),
	}
	for _, entry := range inventories {
		copied := 0
		for key, object := range canonicalObjects {
			current, err := entry.target.Storage.Get(ctx, key, "")
			if err != nil {
				return nil, err
			}
			currentChecksum := ""
			if current != nil {
				currentBytes, err := io.ReadAll(current.Body)
				_ = current.Close()
				if err != nil {
					return nil, err
				}
				currentChecksum = sha256Hex(currentBytes)
			}
			if currentChecksum != "" && currentChecksum != object.checksum {
				return nil, fmt.Errorf("global asset conflict for %s in %s; refusing to overwrite it", key, entry.target.Name)
			}
			if current == nil {
				putArgs := PutArgs{
					Key:         key,
					Body:        object.body,
					ContentType: object.contentType,
					Metadata:    object.metadata,
				}
				if object.cacheControl != "" {
					putArgs.CacheControl = object.cacheControl
				}
				if _, err := entry.target.Storage.Put(ctx, putArgs); err != nil {
					return nil, err
				}
				copied++
			}
			stored, err := readObjectBytes(ctx, entry.target.Storage, key)
			if err != nil {
				return nil, err
			}
			if sha256Hex(stored) != object.checksum {
				return nil, fmt.Errorf("global asset verification failed for %s in %s", key, entry.target.Name)
			}
		}
		manifest := &GlobalAssetManifest{
			Version:     1,
			GeneratedAt: ISOString(time.Now()),
			Assets:      assets,
		}
		payload, err := MarshalJSON(manifest)
		if err != nil {
			return nil, err
		}
		if _, err := entry.target.Storage.Put(ctx, PutArgs{
			Key:          GlobalManifestKey,
			Body:         payload,
			ContentType:  "application/json",
			CacheControl: "public, max-age=300, stale-while-revalidate=86400",
			Metadata: map[string]string{
				"raildrop-access":    "public",
				"raildrop-retention": "permanent",
			},
		}); err != nil {
			return nil, err
		}
		result.Targets[entry.target.Name] = GlobalAssetMirrorTargetResult{Copied: copied, Verified: len(canonicalObjects) + 1}
	}
	return result, nil
}
