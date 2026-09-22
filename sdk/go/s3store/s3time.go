package s3store

import (
	"encoding/xml"
	"time"
)

type s3Time struct {
	time.Time
}

func (value *s3Time) UnmarshalText(data []byte) error {
	parsed, err := time.Parse(time.RFC3339Nano, string(data))
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.999Z", string(data))
	}
	if err != nil {
		return err
	}
	value.Time = parsed
	return nil
}

func parseXML(data []byte, target any) error {
	return xml.Unmarshal(data, target)
}

func trimETag(etag string) string {
	for len(etag) > 0 && etag[0] == '"' {
		etag = etag[1:]
	}
	for len(etag) > 0 && etag[len(etag)-1] == '"' {
		etag = etag[:len(etag)-1]
	}
	return etag
}
