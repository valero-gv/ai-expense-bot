package domain

import (
	"fmt"
	"time"
	"unicode/utf8"
)

func ValidateDate(date string) error {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil || d.Format(time.DateOnly) != date || date < "0001-01-01" || date > "9999-12-31" {
		return fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalid)
	}
	return nil
}

func ValidCategory(category string) bool {
	for _, c := range Categories {
		if category == c {
			return true
		}
	}
	return false
}

func ValidateInput(in Input) error {
	if in.UserID <= 0 || in.UserID > 4_503_599_627_370_495 {
		return fmt.Errorf("%w: user_id must be a positive Telegram ID", ErrInvalid)
	}
	if in.Amount <= 0 || in.Amount > MaxExpense {
		return fmt.Errorf("%w: amount must be between 0.01 and 999999999.99", ErrInvalid)
	}
	if !ValidCategory(in.Category) {
		return fmt.Errorf("%w: unsupported category", ErrInvalid)
	}
	if err := ValidateDate(in.SpentAt); err != nil {
		return err
	}
	if !utf8.ValidString(in.Comment) || utf8.RuneCountInString(in.Comment) > 1000 {
		return fmt.Errorf("%w: comment exceeds 1000 characters", ErrInvalid)
	}
	return nil
}

func ValidateFilter(f Filter) error {
	if f.UserID != nil && (*f.UserID <= 0 || *f.UserID > 4_503_599_627_370_495) {
		return fmt.Errorf("%w: invalid user_id", ErrInvalid)
	}
	if f.From != "" {
		if err := ValidateDate(f.From); err != nil {
			return err
		}
	}
	if f.To != "" {
		if err := ValidateDate(f.To); err != nil {
			return err
		}
	}
	if f.From != "" && f.To != "" && f.From > f.To {
		return fmt.Errorf("%w: from must be on or before to", ErrInvalid)
	}
	if f.Category != "" && !ValidCategory(f.Category) {
		return fmt.Errorf("%w: unsupported category", ErrInvalid)
	}
	if !utf8.ValidString(f.Query) || utf8.RuneCountInString(f.Query) > 200 {
		return fmt.Errorf("%w: query exceeds 200 characters", ErrInvalid)
	}
	if f.Page < 0 || f.Page > 1_000_000 || f.PageSize < 0 || f.PageSize > 100 {
		return fmt.Errorf("%w: page must be 1..1000000 and page_size 1..100", ErrInvalid)
	}
	return nil
}

// Period uses calendar arithmetic in the configured zone, including DST boundaries.
// Both dates are inclusive. A week means the current Monday-through-today week.
func Period(name string, now time.Time, loc *time.Location) (string, string, error) {
	if loc == nil {
		return "", "", fmt.Errorf("%w: timezone is required", ErrInvalid)
	}
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	start, end := today, today
	switch name {
	case "today":
	case "yesterday":
		start = today.AddDate(0, 0, -1)
		end = start
	case "this_week":
		start = today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	case "this_month":
		start = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	case "last_7_days":
		start = today.AddDate(0, 0, -6)
	default:
		return "", "", fmt.Errorf("%w: unsupported period", ErrInvalid)
	}
	return start.Format(time.DateOnly), end.Format(time.DateOnly), nil
}
