package config

import "testing"

func TestConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		bad  bool
	}{
		{"default", map[string]string{"DATABASE_URL": "postgres://local"}, false},
		{"missing db", map[string]string{}, true},
		{"invalid zone", map[string]string{"DATABASE_URL": "x", "APP_TIMEZONE": "invalid"}, true},
		{"unsafe URL", map[string]string{"DATABASE_URL": "x", "LLM_BASE_URL": "http://example.com/v1"}, true},
		{"timeout", map[string]string{"DATABASE_URL": "x", "LLM_TIMEOUT": "0s"}, true},
		{"missing key", map[string]string{"DATABASE_URL": "x", "TELEGRAM_BOT_TOKEN": "test"}, true},
		{"local LLM", map[string]string{"DATABASE_URL": "x", "LLM_BASE_URL": "http://host.docker.internal:11434/v1", "TELEGRAM_BOT_TOKEN": "test", "LLM_API_KEY": "local"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadWith(func(k string) string { return tc.env[k] })
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
		})
	}
}
