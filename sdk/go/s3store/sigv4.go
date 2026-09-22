package s3store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const signingAlgorithm = "AWS4-HMAC-SHA256"

const unsignedPayload = "UNSIGNED-PAYLOAD"

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func signingKey(secretAccessKey string, dateStamp string, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretAccessKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	return hmacSHA256(kService, "aws4_request")
}

const uriUnreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

func uriEncode(value string, encodeSlash bool) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if strings.IndexByte(uriUnreserved, character) >= 0 || (character == '/' && !encodeSlash) {
			builder.WriteByte(character)
			continue
		}
		builder.WriteString("%")
		builder.WriteByte(strings.ToUpper(hex.EncodeToString([]byte{character}))[0])
		builder.WriteByte(strings.ToUpper(hex.EncodeToString([]byte{character}))[1])
	}
	return builder.String()
}

func encodeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	encoded := make([]string, len(segments))
	for index, segment := range segments {
		encoded[index] = uriEncode(segment, false)
	}
	return strings.Join(encoded, "/")
}

func canonicalQuery(params url.Values) string {
	pairs := make([]string, 0, len(params))
	for key, values := range params {
		encodedKey := uriEncode(key, true)
		for _, value := range values {
			pairs = append(pairs, encodedKey+"="+uriEncode(value, true))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func canonicalHeaders(headers map[string]string) (string, string) {
	names := make([]string, 0, len(headers))
	lowered := make(map[string]string, len(headers))
	for name, value := range headers {
		lower := strings.ToLower(name)
		names = append(names, lower)
		lowered[lower] = strings.TrimSpace(value)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteString(":")
		builder.WriteString(lowered[name])
		builder.WriteString("\n")
	}
	return builder.String(), strings.Join(names, ";")
}

func presign(args presignArgs) string {
	now := args.Now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := amzDate[:8]
	scope := dateStamp + "/" + args.Region + "/s3/aws4_request"
	credential := args.AccessKeyID + "/" + scope

	headers := map[string]string{"host": args.Host}
	for name, value := range args.Headers {
		headers[name] = value
	}
	canonicalHeadersBlock, signedHeaders := canonicalHeaders(headers)

	query := url.Values{}
	for key, values := range args.Query {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	query.Set("X-Amz-Algorithm", signingAlgorithm)
	query.Set("X-Amz-Credential", credential)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", itoa(args.ExpiresIn))
	query.Set("X-Amz-SignedHeaders", signedHeaders)

	canonicalRequest := strings.Join([]string{
		args.Method,
		args.Path,
		canonicalQuery(query),
		canonicalHeadersBlock,
		signedHeaders,
		unsignedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		signingAlgorithm,
		amzDate,
		scope,
		sha256HexBytes([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(args.SecretAccessKey, dateStamp, args.Region), stringToSign))
	return args.Scheme + "://" + args.Host + args.Path + "?" + canonicalQuery(query) + "&X-Amz-Signature=" + uriEncode(signature, true)
}

type presignArgs struct {
	Method          string
	Scheme          string
	Host            string
	Path            string
	Query           url.Values
	Headers         map[string]string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ExpiresIn       int
	Now             time.Time
}

func authorize(req *signRequest) {
	now := req.Now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := amzDate[:8]
	scope := dateStamp + "/" + req.Region + "/s3/aws4_request"

	headers := map[string]string{"host": req.Host, "x-amz-date": amzDate, "x-amz-content-sha256": req.PayloadHash}
	for name, value := range req.Headers {
		headers[name] = value
	}
	canonicalHeadersBlock, signedHeaders := canonicalHeaders(headers)

	query := url.Values{}
	for key, values := range req.Query {
		for _, value := range values {
			query.Add(key, value)
		}
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.Path,
		canonicalQuery(query),
		canonicalHeadersBlock,
		signedHeaders,
		req.PayloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		signingAlgorithm,
		amzDate,
		scope,
		sha256HexBytes([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(req.SecretAccessKey, dateStamp, req.Region), stringToSign))
	req.Authorization = signingAlgorithm + " Credential=" + req.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	req.AmzDate = amzDate
}

type signRequest struct {
	Method          string
	Host            string
	Path            string
	Query           url.Values
	Headers         map[string]string
	PayloadHash     string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Now             time.Time
	Authorization   string
	AmzDate         string
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}
