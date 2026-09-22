package s3store

import "encoding/xml"

type initiateMultipartUploadResultXML struct {
	UploadID string `xml:"UploadId"`
}

type completeMultipartUploadXML struct {
	XMLName xml.Name          `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CompleteMultipartUpload"`
	Parts   []completePartXML `xml:"Part"`
}

type completePartXML struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type deleteObjectsXML struct {
	XMLName xml.Name       `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Delete"`
	Quiet   bool           `xml:"Quiet"`
	Objects []deleteKeyXML `xml:"Object"`
}

type deleteKeyXML struct {
	Key string `xml:"Key"`
}

type deleteResultXML struct {
	Errors []deleteErrorXML `xml:"Error"`
}

type deleteErrorXML struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type listBucketResultXML struct {
	IsTruncated           bool          `xml:"IsTruncated"`
	NextContinuationToken string        `xml:"NextContinuationToken"`
	Contents              []contentsXML `xml:"Contents"`
}

type contentsXML struct {
	Key          string `xml:"Key"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	LastModified s3Time `xml:"LastModified"`
}

type listMultipartUploadsResultXML struct {
	IsTruncated        bool        `xml:"IsTruncated"`
	NextKeyMarker      string      `xml:"NextKeyMarker"`
	NextUploadIDMarker string      `xml:"NextUploadIdMarker"`
	Uploads            []uploadXML `xml:"Upload"`
}

type uploadXML struct {
	Key       string `xml:"Key"`
	UploadID  string `xml:"UploadId"`
	Initiated s3Time `xml:"Initiated"`
}

type errorResponseXML struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type corsConfigurationXML struct {
	XMLName xml.Name      `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CORSConfiguration"`
	Rules   []corsRuleXML `xml:"CORSRule"`
}

type corsRuleXML struct {
	AllowedOrigin []string `xml:"AllowedOrigin"`
	AllowedMethod []string `xml:"AllowedMethod"`
	AllowedHeader []string `xml:"AllowedHeader"`
	ExposeHeader  []string `xml:"ExposeHeader"`
	MaxAgeSeconds int      `xml:"MaxAgeSeconds"`
}
