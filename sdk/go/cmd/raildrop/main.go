package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
	"github.com/akpor-kofi/raildrop/sdk/go/s3store"
)

type ManifestFile struct {
	Assets      []ManifestAsset     `json:"assets"`
	Directories []ManifestDirectory `json:"directories,omitempty"`
}

type ManifestAsset struct {
	Alias       string   `json:"alias"`
	Source      string   `json:"source"`
	Filename    string   `json:"filename,omitempty"`
	ContentType string   `json:"contentType"`
	Namespace   []string `json:"namespace"`
}

type ManifestDirectory struct {
	Source      string   `json:"source"`
	AliasPrefix string   `json:"aliasPrefix"`
	ContentType string   `json:"contentType"`
	Namespace   []string `json:"namespace"`
	Extensions  []string `json:"extensions,omitempty"`
}

type mirrorTarget struct {
	name    string
	storage *s3store.RailwayBucketStorage
}

var mirrorEnvironmentNames = []string{"development", "staging", "production"}

var nonAliasCharacters = regexp.MustCompile(`[^a-z0-9-]+`)

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func readMirrorConfig(environment string) (*mirrorTarget, error) {
	prefix := "RAILDROP_" + strings.ToUpper(environment) + "_"
	read := func(name string) string {
		return strings.TrimSpace(os.Getenv(prefix + name))
	}
	bucket := read("BUCKET")
	endpoint := read("ENDPOINT")
	accessKeyID := read("ACCESS_KEY_ID")
	secretAccessKey := read("SECRET_ACCESS_KEY")
	configured := 0
	for _, value := range []string{bucket, endpoint, accessKeyID, secretAccessKey} {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, nil
	}
	if configured != 4 {
		return nil, fmt.Errorf("incomplete %s Raildrop mirror credentials", environment)
	}
	region := read("REGION")
	if region == "" {
		region = "auto"
	}
	storage, err := s3store.NewRailwayBucketStorage(raildrop.BucketConfig{
		Bucket:          bucket,
		Endpoint:        endpoint,
		Region:          region,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		ForcePathStyle:  read("FORCE_PATH_STYLE") == "true",
	}, nil)
	if err != nil {
		return nil, err
	}
	return &mirrorTarget{name: environment, storage: storage}, nil
}

func configuredTargets() ([]*mirrorTarget, error) {
	var targets []*mirrorTarget
	for _, environment := range mirrorEnvironmentNames {
		target, err := readMirrorConfig(environment)
		if err != nil {
			return nil, err
		}
		if target != nil {
			targets = append(targets, target)
		}
	}
	if len(targets) > 0 {
		return targets, nil
	}
	config, err := s3store.BucketConfigFromEnv()
	if err != nil {
		return nil, err
	}
	storage, err := s3store.NewRailwayBucketStorage(config, nil)
	if err != nil {
		return nil, err
	}
	return []*mirrorTarget{{name: "current", storage: storage}}, nil
}

func fileSources(manifestPath string, manifest ManifestFile) ([]raildrop.GlobalAssetSource, error) {
	manifestDirectory := filepath.Dir(manifestPath)
	sources := make([]raildrop.GlobalAssetSource, 0, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		sourcePath := filepath.Join(manifestDirectory, asset.Source)
		body, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, err
		}
		filename := asset.Filename
		if filename == "" {
			filename = filepath.Base(sourcePath)
		}
		sources = append(sources, raildrop.GlobalAssetSource{
			Alias:       asset.Alias,
			Body:        body,
			Filename:    filename,
			ContentType: asset.ContentType,
			Namespace:   asset.Namespace,
		})
	}
	return sources, nil
}

func directorySources(manifestPath string, manifest ManifestFile) ([]raildrop.GlobalAssetSource, error) {
	manifestDirectory := filepath.Dir(manifestPath)
	var sources []raildrop.GlobalAssetSource
	for _, directory := range manifest.Directories {
		sourceDirectory := filepath.Join(manifestDirectory, directory.Source)
		allowedExtensions := make(map[string]bool)
		for _, extension := range directory.Extensions {
			allowedExtensions[strings.ToLower(extension)] = true
		}
		entries, err := os.ReadDir(sourceDirectory)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if len(allowedExtensions) > 0 && !allowedExtensions[extension] {
				continue
			}
			sourcePath := filepath.Join(sourceDirectory, entry.Name())
			body, err := os.ReadFile(sourcePath)
			if err != nil {
				return nil, err
			}
			assetName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			aliasName := nonAliasCharacters.ReplaceAllString(strings.ToLower(assetName), "-")
			sources = append(sources, raildrop.GlobalAssetSource{
				Alias:       directory.AliasPrefix + aliasName,
				Body:        body,
				Filename:    entry.Name(),
				ContentType: directory.ContentType,
				Namespace:   append(append([]string{}, directory.Namespace...), assetName),
			})
		}
	}
	return sources, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	command := os.Args[1]
	argument := ""
	if len(os.Args) > 2 {
		argument = os.Args[2]
	}
	targets, err := configuredTargets()
	if err != nil {
		fail(err)
		return
	}
	ctx := context.Background()
	storage := targets[0].storage
	switch command {
	case "health":
		if _, err := storage.List(ctx, "", ""); err != nil {
			fail(err)
			return
		}
		fmt.Fprintln(os.Stdout, "Raildrop bucket connection is healthy.")
	case "cleanup":
		result, err := raildrop.CleanupExpiredObjects(ctx, storage, timeNow(), 0)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(result)
	case "cors", "cors:apply":
		origins := []string{}
		for _, origin := range strings.Split(os.Getenv("RAILDROP_ALLOWED_ORIGINS"), ",") {
			if trimmed := strings.TrimSpace(origin); trimmed != "" {
				origins = append(origins, trimmed)
			}
		}
		if len(origins) == 0 {
			fail(fmt.Errorf("RAILDROP_ALLOWED_ORIGINS is required"))
			return
		}
		if command == "cors:apply" {
			if err := storage.SetCORSRules(ctx, origins); err != nil {
				fail(err)
				return
			}
		}
		rules, err := storage.GetCORSRules(ctx)
		if err != nil {
			fail(err)
			return
		}
		configured := make(map[string]bool)
		for _, rule := range rules {
			for _, origin := range rule.AllowedOrigins {
				configured[origin] = true
			}
		}
		var missing []string
		for _, origin := range origins {
			if !configured[origin] {
				missing = append(missing, origin)
			}
		}
		var invalid []string
		requiredMethods := []string{"PUT", "GET", "HEAD"}
		for _, origin := range origins {
			valid := false
			for _, rule := range rules {
				originAllowed := false
				for _, allowed := range rule.AllowedOrigins {
					if allowed == origin {
						originAllowed = true
						break
					}
				}
				if !originAllowed {
					continue
				}
				methodsAllowed := true
				for _, method := range requiredMethods {
					found := false
					for _, allowed := range rule.AllowedMethods {
						if allowed == method {
							found = true
							break
						}
					}
					if !found {
						methodsAllowed = false
						break
					}
				}
				headersAllowed := false
				for _, allowed := range rule.AllowedHeaders {
					if allowed == "*" {
						headersAllowed = true
						break
					}
				}
				if methodsAllowed && headersAllowed {
					valid = true
					break
				}
			}
			if !valid {
				invalid = append(invalid, origin)
			}
		}
		writeJSON(map[string]any{"origins": origins, "missing": missing, "invalid": invalid, "rules": rules})
		if len(missing) > 0 || len(invalid) > 0 {
			os.Exit(1)
		}
	case "sync", "upsert":
		if argument == "" {
			usage()
			return
		}
		manifestPath, err := filepath.Abs(argument)
		if err != nil {
			fail(err)
			return
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			fail(err)
			return
		}
		var manifest ManifestFile
		if err := json.Unmarshal(raw, &manifest); err != nil {
			fail(err)
			return
		}
		sources, err := fileSources(manifestPath, manifest)
		if err != nil {
			fail(err)
			return
		}
		directories, err := directorySources(manifestPath, manifest)
		if err != nil {
			fail(err)
			return
		}
		sources = append(sources, directories...)
		results := make(map[string]*raildrop.GlobalAssetSyncResult)
		for _, target := range targets {
			var result *raildrop.GlobalAssetSyncResult
			var syncErr error
			if command == "upsert" {
				result, syncErr = raildrop.UpsertGlobalAssets(ctx, target.storage, sources)
			} else {
				result, syncErr = raildrop.SyncGlobalAssets(ctx, target.storage, sources)
			}
			if syncErr != nil {
				fail(syncErr)
				return
			}
			results[target.name] = result
		}
		writeJSON(results)
	case "mirror":
		mirrorTargets := make([]raildrop.GlobalAssetMirrorTarget, 0, len(targets))
		for _, target := range targets {
			mirrorTargets = append(mirrorTargets, raildrop.GlobalAssetMirrorTarget{Name: target.name, Storage: target.storage})
		}
		result, err := raildrop.MirrorGlobalAssets(ctx, mirrorTargets)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(result)
	default:
		usage()
	}
}

func timeNow() time.Time {
	return time.Now()
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: raildrop health | cleanup | cors | cors:apply | sync <manifest.json> | upsert <manifest.json> | mirror")
	os.Exit(1)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
