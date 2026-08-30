package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/cloudwego/hertz/pkg/app"
)

const requestIDKey = "xiaolanhe.request_id"

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"requestId,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

func RequestIDMiddleware(ctx context.Context, c *app.RequestContext) {
	requestID := string(c.Request.Header.Peek("X-Request-ID"))
	if !safeRequestID.MatchString(requestID) {
		requestID = newRequestID()
	}
	c.Set(requestIDKey, requestID)
	c.Header("X-Request-ID", requestID)
	c.Next(ctx)
}

func RequestID(c *app.RequestContext) string {
	value, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	requestID, _ := value.(string)
	return requestID
}

func WriteError(c *app.RequestContext, status int, code, message string, fields map[string]string) {
	c.JSON(status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, RequestID: RequestID(c), Fields: fields}})
}

func DecodeJSON(body []byte, limit int, target any) error {
	if len(body) == 0 || len(body) > limit {
		return errors.New("invalid body size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request"
	}
	return hex.EncodeToString(value[:])
}
