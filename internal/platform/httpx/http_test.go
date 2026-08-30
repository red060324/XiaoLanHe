package httpx

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestDecodeJSON(t *testing.T) {
	var value struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON([]byte(`{"name":"game"}`), 32, &value); err != nil || value.Name != "game" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	for _, body := range [][]byte{nil, []byte(`{"unknown":1}`), []byte(`{"name":"a"}{"name":"b"}`), make([]byte, 33)} {
		if err := DecodeJSON(body, 32, &value); err == nil {
			t.Fatalf("body %q should fail", body)
		}
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	for _, test := range []struct {
		incoming string
		want     string
	}{
		{incoming: "safe-id_1", want: "safe-id_1"},
		{incoming: "unsafe id"},
	} {
		c := app.NewContext(0)
		c.Request.Header.Set("X-Request-ID", test.incoming)
		RequestIDMiddleware(context.Background(), c)
		got := RequestID(c)
		if got == "" || string(c.Response.Header.Peek("X-Request-ID")) != got || (test.want != "" && got != test.want) || (test.want == "" && got == test.incoming) {
			t.Fatalf("incoming=%q got=%q header=%q", test.incoming, got, c.Response.Header.Peek("X-Request-ID"))
		}
	}
}

func TestWriteError(t *testing.T) {
	c := app.NewContext(0)
	c.Set(requestIDKey, "request-1")
	WriteError(c, 400, "invalid_request", "bad", map[string]string{"name": "required"})
	var body ErrorBody
	if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if c.Response.StatusCode() != 400 || body.Error.RequestID != "request-1" || body.Error.Fields["name"] != "required" {
		t.Fatalf("status=%d body=%#v", c.Response.StatusCode(), body)
	}
}
