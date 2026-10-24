package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"expensebot/internal/agent"
	"expensebot/internal/api"
	"expensebot/internal/config"
	"expensebot/internal/store"
	"expensebot/internal/telegram"
	"expensebot/web"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	health := flag.Bool("healthcheck", false, "check the local HTTP server")
	flag.Parse()
	if *health {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			os.Exit(1)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("invalid database configuration")
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(startup); err != nil {
		return fmt.Errorf("database is unavailable")
	}
	if err := store.Migrate(startup, db); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	repo := store.New(db)
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: api.New(repo, web.Files), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var poll func(context.Context) error
	if cfg.TelegramToken != "" {
		llm := agent.NewClient(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMAPIKey, &http.Client{Timeout: cfg.LLMTimeout})
		engine := agent.NewEngine(repo, llm, agent.Config{Location: cfg.Location, Timeout: cfg.LLMTimeout})
		bot := telegram.New(cfg.TelegramToken, nil, engine, log)
		poll = bot.Run
		log.Info("Telegram polling enabled", "model", cfg.LLMModel, "timezone", cfg.Location.String())
	} else {
		log.Info("Telegram polling disabled; configure TELEGRAM_BOT_TOKEN and LLM_API_KEY to enable")
	}
	return serve(ctx, srv, func() error {
		log.Info("HTTP server starting", "address", cfg.ListenAddr)
		return srv.ListenAndServe()
	}, poll, 10*time.Second)
}
