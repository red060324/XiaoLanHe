package postgrestomysql

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

func normalizeRow(table tableSpec, values []any) (Row, error) {
	if len(values) != len(table.columns) {
		return nil, fmt.Errorf("%s returned %d columns, want %d", table.name, len(values), len(table.columns))
	}
	row := make(Row, len(values))
	for i, value := range values {
		cell, err := normalizeCell(table.columns[i].kind, value)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", table.name, table.columns[i].name, err)
		}
		row[i] = cell
	}
	return row, nil
}

func normalizeCell(kind cellKind, value any) (Cell, error) {
	if value == nil {
		return Cell{Kind: kind, Null: true}, nil
	}
	cell := Cell{Kind: kind}
	switch kind {
	case kindString:
		switch value := value.(type) {
		case string:
			cell.Value = value
		case []byte:
			cell.Value = string(value)
		default:
			return Cell{}, fmt.Errorf("expected text, got %T", value)
		}
	case kindInt:
		integer, err := exactInt64(value)
		if err != nil {
			return Cell{}, err
		}
		cell.Value = strconv.FormatInt(integer, 10)
	case kindTime:
		instant, ok := value.(time.Time)
		if !ok {
			return Cell{}, fmt.Errorf("expected time, got %T", value)
		}
		instant = instant.UTC()
		if instant.Year() < 1000 || instant.Year() > 9999 || instant.Nanosecond()%1000 != 0 {
			return Cell{}, fmt.Errorf("time %s cannot be represented exactly as UTC DATETIME(6)", instant.Format(time.RFC3339Nano))
		}
		cell.Value = instant.Format(time.RFC3339Nano)
	case kindDate:
		instant, ok := value.(time.Time)
		if !ok {
			return Cell{}, fmt.Errorf("expected date, got %T", value)
		}
		if instant.Year() < 1000 || instant.Year() > 9999 {
			return Cell{}, fmt.Errorf("date %s is outside MySQL DATE range", instant.Format("2006-01-02"))
		}
		cell.Value = instant.Format("2006-01-02")
	case kindJSON:
		canonical, err := canonicalJSON(value)
		if err != nil {
			return Cell{}, err
		}
		cell.Value = canonical
	case kindBinary:
		binary, ok := value.([]byte)
		if !ok {
			return Cell{}, fmt.Errorf("expected binary value, got %T", value)
		}
		if len(binary) != sha256Bytes {
			return Cell{}, fmt.Errorf("binary digest has %d bytes, want %d", len(binary), sha256Bytes)
		}
		cell.Value = hex.EncodeToString(binary)
	default:
		return Cell{}, fmt.Errorf("unsupported cell kind %q", kind)
	}
	return cell, nil
}

const sha256Bytes = 32

func exactInt64(value any) (int64, error) {
	switch value := value.(type) {
	case int64:
		return value, nil
	case int32:
		return int64(value), nil
	case int16:
		return int64(value), nil
	case int:
		return int64(value), nil
	case uint64:
		if value > math.MaxInt64 {
			return 0, fmt.Errorf("integer exceeds int64")
		}
		return int64(value), nil
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	case string:
		return strconv.ParseInt(value, 10, 64)
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func canonicalJSON(value any) (string, error) {
	var source []byte
	switch value := value.(type) {
	case []byte:
		source = value
	case string:
		source = []byte(value)
	case json.RawMessage:
		source = value
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode JSON: %w", err)
		}
		source = encoded
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", fmt.Errorf("decode JSON: trailing value")
		}
		return "", fmt.Errorf("decode JSON trailer: %w", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("canonicalize JSON: %w", err)
	}
	return string(canonical), nil
}
