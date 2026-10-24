package domain

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExactMoney(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		value   Money
	}{
		{"350", "350.00", 35000}, {"0.1", "0.10", 10}, {"450,25", "450.25", 45025},
		{"999999999.99", "999999999.99", MaxExpense}, {"92233720368547758.07", "92233720368547758.07", Money(math.MaxInt64)},
	} {
		m, err := ParseMoney(tc.in)
		if err != nil || m != tc.value || m.String() != tc.out {
			t.Fatalf("%q: %v %v", tc.in, m, err)
		}
		b, _ := json.Marshal(m)
		var decoded Money
		if err := json.Unmarshal(b, &decoded); err != nil || decoded != m {
			t.Fatalf("roundtrip %s: %v", b, err)
		}
		var scanned Money
		if err := scanned.Scan([]byte(tc.out)); err != nil || scanned != m {
			t.Fatal("SQL exact roundtrip", err)
		}
	}
	a, _ := ParseMoney("0.10")
	b, _ := ParseMoney("0.20")
	if (a + b).String() != "0.30" {
		t.Fatal("decimal precision lost")
	}
	for _, bad := range []string{"", "-1", "+1", "1e2", "NaN", "1.001", ".1", "1.", "1,2.3", " 1", "1 ", "1_000", "92233720368547758.08"} {
		if _, err := ParseMoney(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []string{"1.23", "null", "{}", `"1.234"`} {
		var m Money
		if json.Unmarshal([]byte(bad), &m) == nil {
			t.Errorf("accepted JSON %q", bad)
		}
	}
	var m Money
	if m.Scan(0.1) == nil {
		t.Fatal("accepted float SQL")
	}
}

func TestValidation(t *testing.T) {
	valid := Input{UserID: 123, Amount: 1, Category: Food, SpentAt: "2024-02-29"}
	if err := ValidateInput(valid); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Input){
		func(i *Input) { i.UserID = 0 }, func(i *Input) { i.Amount = 0 }, func(i *Input) { i.Amount = MaxExpense + 1 },
		func(i *Input) { i.Category = "coffee" }, func(i *Input) { i.SpentAt = "2023-02-29" }, func(i *Input) { i.SpentAt = "2026-2-01" },
		func(i *Input) { i.Comment = "coffee\x00receipt" },
	} {
		i := valid
		edit(&i)
		if ValidateInput(i) == nil {
			t.Errorf("accepted %+v", i)
		}
	}
	for _, f := range []Filter{{From: "2026-02-30"}, {From: "2026-10-16", To: "2026-10-15"}, {Category: "x"}, {Page: -1}, {PageSize: 101}, {Query: "coffee\x00receipt"}} {
		if ValidateFilter(f) == nil {
			t.Errorf("accepted filter %+v", f)
		}
	}
}

func TestCategoryLabelsPreserveStoredKeys(t *testing.T) {
	wantKeys := []string{Food, Transport, Housing, Entertainment, Health, Shopping, Other}
	if !reflect.DeepEqual(Categories, wantKeys) {
		t.Fatalf("category order changed: %v", Categories)
	}
	wantLabels := []string{"Food", "Transport", "Housing", "Entertainment", "Health", "Shopping", "Other"}
	for i, key := range wantKeys {
		if !ValidCategory(key) {
			t.Errorf("stored category is invalid: %q", key)
		}
		if label := CategoryLabel(key); label != wantLabels[i] {
			t.Errorf("CategoryLabel(%q) = %q, want %q", key, label, wantLabels[i])
		}
		if ValidCategory(wantLabels[i]) {
			t.Errorf("display label accepted as a stored key: %q", wantLabels[i])
		}
	}
	for _, unknown := range []string{"", "custom category"} {
		if CategoryLabel(unknown) != unknown {
			t.Errorf("unknown category was changed: %q", unknown)
		}
	}
}

func TestTextValidationRejectsUnsupportedCharactersAndHonorsRuneLimits(t *testing.T) {
	valid := Input{UserID: 123, Amount: 1, Category: Food, SpentAt: "2026-10-16"}
	valid.Comment = strings.Repeat("é", 1000)
	if err := ValidateInput(valid); err != nil {
		t.Fatalf("1000 Unicode characters rejected: %v", err)
	}
	for _, comment := range []string{"\x00", "coffee\x00receipt", string([]byte{0xff}), strings.Repeat("x", 1001)} {
		in := valid
		in.Comment = comment
		if err := ValidateInput(in); err == nil {
			t.Errorf("invalid comment accepted: %q", comment)
		}
	}
	if err := ValidateFilter(Filter{Query: strings.Repeat("é", 200)}); err != nil {
		t.Fatalf("200 Unicode query characters rejected: %v", err)
	}
	for _, query := range []string{"\x00", "coffee\x00receipt", string([]byte{0xff}), strings.Repeat("x", 201)} {
		if err := ValidateFilter(Filter{Query: query}); err == nil {
			t.Errorf("invalid query accepted: %q", query)
		}
	}
}

func TestPeriods(t *testing.T) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	berlin, _ := time.LoadLocation("Europe/Berlin")
	for _, tc := range []struct {
		at, period, from, to string
		loc                  *time.Location
	}{
		{"2026-01-01T00:10:00+03:00", "yesterday", "2025-12-31", "2025-12-31", moscow},
		{"2026-01-04T20:00:00Z", "this_week", "2025-12-29", "2026-01-04", moscow},
		{"2026-01-04T21:01:00Z", "this_week", "2026-01-05", "2026-01-05", moscow},
		{"2024-03-01T00:01:00+03:00", "yesterday", "2024-02-29", "2024-02-29", moscow},
		{"2026-03-29T23:30:00+02:00", "last_7_days", "2026-03-23", "2026-03-29", berlin},
		{"2026-10-25T23:30:00+01:00", "this_month", "2026-10-01", "2026-10-25", berlin},
	} {
		n, _ := time.Parse(time.RFC3339, tc.at)
		from, to, err := Period(tc.period, n, tc.loc)
		if err != nil || from != tc.from || to != tc.to {
			t.Errorf("%s %s: %s %s %v", tc.at, tc.period, from, to, err)
		}
	}
	if _, _, err := Period("unknown", time.Now(), moscow); err == nil {
		t.Fatal("unknown period accepted")
	}
}
