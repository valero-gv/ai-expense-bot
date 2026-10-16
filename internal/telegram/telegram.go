// Package telegram polls Telegram directly. It exposes no public update endpoint.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type Processor interface {
	Process(context.Context, int64, int64, string, string) (string, bool, error)
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		From *User `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type Bot struct {
	baseURL string
	client  *http.Client
	engine  Processor
	log     *slog.Logger
}

func New(token string, client *http.Client, engine Processor, log *slog.Logger) *Bot {
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Bot{baseURL: "https://api.telegram.org/bot" + token, client: client, engine: engine, log: log}
}

// Handle accepts only private sender-owned chats. Owner identity never comes from text or the LLM.
func (b *Bot) Handle(ctx context.Context, u Update) error {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID <= 0 || m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
		return nil
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return nil
	}
	if strings.Fields(text)[0] == "/start" {
		return b.call(ctx, "sendMessage", map[string]any{
			"chat_id": m.Chat.ID,
			"text":    "Здравствуйте! Я помогу учитывать ваши расходы. Напишите, например: «кофе 350», «такси 1200 вчера», «покажи траты за неделю», «последнее было не 350, а 450» или «удали такси».",
		}, nil)
	}
	label := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	if m.From.Username != "" {
		label += " (@" + m.From.Username + ")"
	}
	reply, _, err := b.engine.Process(ctx, u.ID, m.From.ID, label, text)
	if err != nil {
		// Do not log provider bodies, user text, tokens or URL errors (Telegram URLs contain the token).
		b.log.Warn("agent turn failed; transaction rolled back", "update_id", u.ID)
		reply = "Не удалось обработать запрос. Изменения не сохранены. Попробуйте ещё раз."
	}
	return b.call(ctx, "sendMessage", map[string]any{"chat_id": m.Chat.ID, "text": truncate(reply, 4000)}, nil)
}

func (b *Bot) Run(ctx context.Context) error {
	var offset int64
	for ctx.Err() == nil {
		var updates []Update
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "limit": 50, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.log.Warn("Telegram polling unavailable; retrying", "error", err)
			if !pause(ctx, 3*time.Second) {
				break
			}
			continue
		}
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			// On send failure do not acknowledge this update. A retry reads the saved DB reply.
			for ctx.Err() == nil {
				err := b.Handle(ctx, u)
				if err == nil {
					break
				}
				b.log.Warn("Telegram reply unavailable; retrying", "update_id", u.ID, "error", err)
				if !pause(ctx, 3*time.Second) {
					break
				}
			}
			if ctx.Err() != nil {
				break
			}
			offset = u.ID + 1
		}
	}
	return ctx.Err()
}

func (b *Bot) call(ctx context.Context, method string, data any, out any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("Telegram request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Telegram request configuration failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		// URL errors contain the bot token. Keep only a safe failure category.
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("Telegram request canceled")
		}
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			return fmt.Errorf("Telegram transport timed out")
		}
		return fmt.Errorf("Telegram transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil || !envelope.OK {
		return fmt.Errorf("Telegram response failed")
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("Telegram response decoding failed")
		}
	}
	return nil
}

func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
