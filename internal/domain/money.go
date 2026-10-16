package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Money is an exact number of kopecks. Floats are never used, including JSON and SQL.
type Money int64

const MaxExpense Money = 99_999_999_999 // 999,999,999.99 RUB

func ParseMoney(s string) (Money, error) {
	if len(s) == 0 || len(s) > 22 || strings.TrimSpace(s) != s {
		return 0, fmt.Errorf("%w: amount must be a decimal string", ErrInvalid)
	}
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("%w: amount must have at most two decimals", ErrInvalid)
	}
	for _, p := range parts {
		if p == "" {
			return 0, fmt.Errorf("%w: empty amount digits", ErrInvalid)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("%w: amount must contain decimal digits", ErrInvalid)
			}
		}
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, fmt.Errorf("%w: amount must have at most two decimals", ErrInvalid)
		}
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	// Parsing the concatenated integer also detects overflow before multiplication.
	cents, err := strconv.ParseInt(parts[0]+fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: amount too large", ErrInvalid)
	}
	return Money(cents), nil
}

func (m Money) String() string {
	v := int64(m)
	if v < 0 { // Validation disallows negative expenses; keep formatting safe for all int64 values.
		u := uint64(-(v + 1)) + 1
		return fmt.Sprintf("-%d.%02d", u/100, u%100)
	}
	return fmt.Sprintf("%d.%02d", v/100, v%100)
}

func (m Money) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

func (m *Money) UnmarshalJSON(data []byte) error {
	var s string
	if len(data) == 0 || data[0] != '"' || json.Unmarshal(data, &s) != nil {
		return fmt.Errorf("%w: amount must be a JSON string, e.g. \"350.00\"", ErrInvalid)
	}
	parsed, err := ParseMoney(s)
	if err == nil {
		*m = parsed
	}
	return err
}

func (m Money) Value() (driver.Value, error) { return m.String(), nil }

func (m *Money) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("unsupported exact money SQL value %T", src)
	}
	v, err := ParseMoney(s)
	if err == nil {
		*m = v
	}
	return err
}
