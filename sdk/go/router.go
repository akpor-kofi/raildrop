package raildrop

import (
	"context"
	"encoding/json"
	"net/http"
)

type Rule struct {
	MaxFileSize      string
	MaxFileSizeBytes int64
	MaxFileCount     int
	MimeTypes        []string
}

type Rules map[string]Rule

type PolicyOverride struct {
	Access              Access    `json:"access,omitempty"`
	Retention           Retention `json:"retention,omitempty"`
	TemporaryTTLSeconds *int64    `json:"temporaryTtlSeconds,omitempty"`
}

type UploadedFileInfo struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Access    Access    `json:"access"`
	Retention Retention `json:"retention"`
	PublicURL *string   `json:"publicUrl"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Size      int64     `json:"size"`
	ETag      *string   `json:"etag"`
	ExpiresAt *string   `json:"expiresAt"`
}

type UploadedFile struct {
	UploadedFileInfo
	ServerData any `json:"serverData,omitempty"`
}

type MiddlewareArgs struct {
	Request *http.Request
	Input   json.RawMessage
	Files   []RequestedFile
}

type NamespaceArgs struct {
	Input    json.RawMessage
	Metadata json.RawMessage
	File     RequestedFile
}

type ValidateArgs struct {
	File     UploadedFileInfo
	Stored   *StoredObject
	Metadata json.RawMessage
}

type CompletionArgs struct {
	Input    json.RawMessage
	Metadata json.RawMessage
	File     UploadedFileInfo
}

type UploadErrorArgs struct {
	Error    error
	Input    json.RawMessage
	Metadata json.RawMessage
}

type MiddlewareFunc func(ctx context.Context, args MiddlewareArgs) (json.RawMessage, error)

type NamespaceFunc func(args NamespaceArgs) ([]string, error)

type ValidateFunc func(args ValidateArgs) error

type CompleteFunc func(ctx context.Context, args CompletionArgs) (any, error)

type ErrorFunc func(args UploadErrorArgs) error

type Route struct {
	Rules               Rules
	Access              Access
	Retention           Retention
	TemporaryTTLSeconds int64
	Middleware          MiddlewareFunc
	Namespace           NamespaceFunc
	Validate            ValidateFunc
	OnUploadComplete    CompleteFunc
	OnUploadError       ErrorFunc
}

type Router map[string]*Route

type RouteDefinition[Input any, Metadata any, Output any] struct {
	Middleware       func(ctx context.Context, request *http.Request, input Input, files []RequestedFile) (Metadata, error)
	Namespace        func(input Input, metadata Metadata, file RequestedFile) ([]string, error)
	Validate         func(file UploadedFileInfo, stored *StoredObject, metadata Metadata) error
	OnUploadComplete func(ctx context.Context, file UploadedFileInfo, input Input, metadata Metadata) (Output, error)
	OnUploadError    func(err error, input Input, metadata Metadata) error
}

type RouteOptions struct {
	Access              Access
	Retention           Retention
	TemporaryTTLSeconds int64
}

type RouteOption func(*RouteOptions)

func WithAccess(access Access) RouteOption {
	return func(options *RouteOptions) { options.Access = access }
}

func WithRetention(retention Retention, ttlSeconds int64) RouteOption {
	return func(options *RouteOptions) {
		options.Retention = retention
		if ttlSeconds > 0 {
			options.TemporaryTTLSeconds = ttlSeconds
		}
	}
}

func WithTemporaryTTL(ttlSeconds int64) RouteOption {
	return func(options *RouteOptions) { options.TemporaryTTLSeconds = ttlSeconds }
}

const DefaultTemporaryTTLSeconds = 7 * 24 * 60 * 60

func DefineRoute[Input any, Metadata any, Output any](
	rules Rules,
	definition RouteDefinition[Input, Metadata, Output],
	options ...RouteOption,
) *Route {
	resolved := RouteOptions{
		Access:              AccessPublic,
		Retention:           RetentionPermanent,
		TemporaryTTLSeconds: DefaultTemporaryTTLSeconds,
	}
	for _, option := range options {
		option(&resolved)
	}
	route := &Route{
		Rules:               rules,
		Access:              resolved.Access,
		Retention:           resolved.Retention,
		TemporaryTTLSeconds: resolved.TemporaryTTLSeconds,
		Namespace: func(args NamespaceArgs) ([]string, error) {
			var zeroInput Input
			if args.Input != nil {
				if err := json.Unmarshal(args.Input, &zeroInput); err != nil {
					return nil, WrapError(CodeBadRequest, "Invalid route input.", err)
				}
			}
			var metadata Metadata
			if args.Metadata != nil {
				if err := json.Unmarshal(args.Metadata, &metadata); err != nil {
					return nil, WrapError(CodeBadRequest, "Invalid route metadata.", err)
				}
			}
			if definition.Namespace == nil {
				return []string{"uploads"}, nil
			}
			return definition.Namespace(zeroInput, metadata, args.File)
		},
		Validate: func(args ValidateArgs) error {
			if definition.Validate == nil {
				return nil
			}
			var metadata Metadata
			if args.Metadata != nil {
				if err := json.Unmarshal(args.Metadata, &metadata); err != nil {
					return WrapError(CodeBadRequest, "Invalid route metadata.", err)
				}
			}
			return definition.Validate(args.File, args.Stored, metadata)
		},
	}
	if definition.Middleware != nil {
		route.Middleware = func(ctx context.Context, args MiddlewareArgs) (json.RawMessage, error) {
			var input Input
			if args.Input != nil {
				if err := json.Unmarshal(args.Input, &input); err != nil {
					return nil, WrapError(CodeBadRequest, "Invalid route input.", err)
				}
			}
			metadata, err := definition.Middleware(ctx, args.Request, input, args.Files)
			if err != nil {
				return nil, err
			}
			return MarshalJSON(metadata)
		}
	} else {
		route.Middleware = func(context.Context, MiddlewareArgs) (json.RawMessage, error) {
			return json.RawMessage("{}"), nil
		}
	}
	if definition.OnUploadComplete != nil {
		route.OnUploadComplete = func(ctx context.Context, args CompletionArgs) (any, error) {
			var input Input
			if args.Input != nil {
				if err := json.Unmarshal(args.Input, &input); err != nil {
					return nil, WrapError(CodeBadRequest, "Invalid route input.", err)
				}
			}
			var metadata Metadata
			if args.Metadata != nil {
				if err := json.Unmarshal(args.Metadata, &metadata); err != nil {
					return nil, WrapError(CodeBadRequest, "Invalid route metadata.", err)
				}
			}
			return definition.OnUploadComplete(ctx, args.File, input, metadata)
		}
	}
	if definition.OnUploadError != nil {
		route.OnUploadError = func(args UploadErrorArgs) error {
			var input Input
			if args.Input != nil {
				_ = json.Unmarshal(args.Input, &input)
			}
			var metadata Metadata
			if args.Metadata != nil {
				_ = json.Unmarshal(args.Metadata, &metadata)
			}
			return definition.OnUploadError(args.Error, input, metadata)
		}
	}
	return route
}

func WithPolicy(metadata any, policy PolicyOverride) (any, error) {
	source, ok := metadata.(map[string]any)
	if !ok {
		raw, err := MarshalJSON(metadata)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, WrapError(CodeBadRequest, "Policy metadata must be a JSON object.", err)
		}
	}
	merged := make(map[string]any, len(source)+1)
	for key, value := range source {
		merged[key] = value
	}
	merged["$raildrop"] = policy
	return merged, nil
}

func PolicyFromMetadata(metadata json.RawMessage) PolicyOverride {
	var envelope struct {
		Raildrop *struct {
			Access              *Access    `json:"access"`
			Retention           *Retention `json:"retention"`
			TemporaryTTLSeconds *float64   `json:"temporaryTtlSeconds"`
		} `json:"$raildrop"`
	}
	if len(metadata) == 0 {
		return PolicyOverride{}
	}
	if err := json.Unmarshal(metadata, &envelope); err != nil || envelope.Raildrop == nil {
		return PolicyOverride{}
	}
	policy := envelope.Raildrop
	override := PolicyOverride{}
	if policy.Access != nil {
		if *policy.Access == AccessPublic || *policy.Access == AccessPrivate {
			override.Access = *policy.Access
		}
	}
	if policy.Retention != nil {
		if *policy.Retention == RetentionPermanent || *policy.Retention == RetentionTemporary {
			override.Retention = *policy.Retention
		}
	}
	if policy.TemporaryTTLSeconds != nil {
		value := int64(*policy.TemporaryTTLSeconds)
		override.TemporaryTTLSeconds = &value
	}
	return override
}
