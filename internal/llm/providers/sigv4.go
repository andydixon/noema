package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 over the standard library. Only what Bedrock
// needs: POST with a JSON body, a handful of headers, no query string. The
// canonical URI is the request path URI-encoded a second time, as the
// specification requires for every service except S3.

type awsCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// parseAWSCredentials reads "ACCESS_KEY_ID:SECRET_ACCESS_KEY[:SESSION_TOKEN]"
// from a resolved secret reference. One reference keeps the whole
// credential encrypted together; the secret never leaves this package.
func parseAWSCredentials(s string) (awsCredentials, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return awsCredentials{}, false
	}
	c := awsCredentials{AccessKeyID: parts[0], SecretAccessKey: parts[1]}
	if len(parts) == 3 {
		c.SessionToken = parts[2]
	}
	return c, true
}

const (
	amzDateFormat = "20060102T150405Z"
	amzShortDate  = "20060102"
)

// signV4 adds X-Amz-Date, optionally X-Amz-Security-Token, and Authorization
// to req for the given body. The Host header is signed; Content-Type is
// signed when present.
func signV4(req *http.Request, body []byte, creds awsCredentials, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host, "x-amz-date": amzDate}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = strings.TrimSpace(ct)
	}
	if creds.SessionToken != "" {
		headers["x-amz-security-token"] = creds.SessionToken
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	payload := sha256.Sum256(body)
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, canonicalURI(path), canonicalQuery(req.URL.RawQuery), canonHeaders.String(), signed, hex.EncodeToString(payload[:])}, "\n")
	scope := t.Format(amzShortDate) + "/" + region + "/" + service + "/aws4_request"
	cr := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(cr[:])}, "\n")
	kDate := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), t.Format(amzShortDate))
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+creds.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalURI URI-encodes an already-encoded path once more, keeping '/'
// and unreserved characters, which is what SigV4 requires outside S3.
func canonicalURI(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '/', (c >= 'A' && c <= 'Z'), (c >= 'a' && c <= 'z'), (c >= '0' && c <= '9'), c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// canonicalQuery sorts and encodes query parameters. Bedrock calls send none,
// but the function keeps the signer complete.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}
