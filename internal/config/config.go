package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	DatabaseURL, ListenAddr, TelegramToken, LLMBaseURL, LLMModel, LLMAPIKey string
	Location                                                                *time.Location
	LLMTimeout                                                              time.Duration
}

func Load() (Config, error) { return LoadWith(os.Getenv) }

func LoadWith(get func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := get(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		DatabaseURL:   get("DATABASE_URL"),
		ListenAddr:    value("LISTEN_ADDR", "127.0.0.1:8080"),
		TelegramToken: get("TELEGRAM_BOT_TOKEN"),
		LLMBaseURL:    value("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:      value("LLM_MODEL", "gpt-4.1-mini"),
		LLMAPIKey:     get("LLM_API_KEY"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	var err error
	c.Location, err = time.LoadLocation(value("APP_TIMEZONE", "Europe/Moscow"))
	if err != nil {
		return c, fmt.Errorf("APP_TIMEZONE must be an IANA timezone")
	}
	c.LLMTimeout, err = time.ParseDuration(value("LLM_TIMEOUT", "30s"))
	if err != nil || c.LLMTimeout < time.Second || c.LLMTimeout > 2*time.Minute {
		return c, fmt.Errorf("LLM_TIMEOUT must be between 1s and 2m")
	}
	u, err := url.Parse(c.LLMBaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("LLM_BASE_URL must be an HTTP(S) API URL without credentials or query")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && u.Hostname() != "host.docker.internal" {
		return c, fmt.Errorf("LLM_BASE_URL requires HTTPS except for a local provider")
	}
	if strings.TrimSpace(c.LLMModel) == "" {
		return c, fmt.Errorf("LLM_MODEL is required")
	}
	if c.TelegramToken != "" && c.LLMAPIKey == "" {
		return c, fmt.Errorf("LLM_API_KEY is required when TELEGRAM_BOT_TOKEN is set (local providers may use a nonsecret placeholder)")
	}
	return c, nil
}
