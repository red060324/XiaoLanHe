package rocketmq

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

const maxEventBytes = 2048

type wireEvent struct {
	Version           int    `json:"version"`
	RequestID         string `json:"requestId"`
	ActivityID        int64  `json:"activityId"`
	ActivityVersion   int64  `json:"activityVersion"`
	UserID            int64  `json:"userId"`
	ReservedAt        string `json:"reservedAt"`
	IdempotencyDigest string `json:"idempotencyDigest"`
}

func encodeEvent(event flashsale.Event) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(wireEvent{
		Version: event.Version, RequestID: event.RequestID, ActivityID: event.ActivityID,
		ActivityVersion: event.ActivityVersion, UserID: event.UserID, ReservedAt: event.ReservedAt.UTC().Format(time.RFC3339Nano),
		IdempotencyDigest: event.IdempotencyDigest,
	})
	if err != nil || len(payload) > maxEventBytes {
		return nil, flashsale.ErrUnsupportedEvent
	}
	return payload, nil
}

func decodeEvent(payload []byte) (flashsale.Event, error) {
	if len(payload) == 0 || len(payload) > maxEventBytes {
		return flashsale.Event{}, flashsale.ErrUnsupportedEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire wireEvent
	if err := decoder.Decode(&wire); err != nil {
		return flashsale.Event{}, flashsale.ErrUnsupportedEvent
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return flashsale.Event{}, flashsale.ErrUnsupportedEvent
	}
	reservedAt, err := time.Parse(time.RFC3339Nano, wire.ReservedAt)
	if err != nil {
		return flashsale.Event{}, flashsale.ErrUnsupportedEvent
	}
	event := flashsale.Event{
		Version: wire.Version, RequestID: wire.RequestID, ActivityID: wire.ActivityID, ActivityVersion: wire.ActivityVersion,
		UserID: wire.UserID, ReservedAt: reservedAt.UTC(), IdempotencyDigest: wire.IdempotencyDigest,
	}
	if err := event.Validate(); err != nil {
		return flashsale.Event{}, flashsale.ErrUnsupportedEvent
	}
	return event, nil
}
