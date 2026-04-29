package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	port := envOr("PORT", "8080")
	dbPath := envOr("DB_PATH", "profiles.db")

	secret := os.Getenv("TOKEN_SECRET")
	if secret == "" {
		secret = randomURLSafe(32)
		log.Println("warning: TOKEN_SECRET not set; generated ephemeral secret (tokens won't survive restart)")
	}

	store, err := OpenStore(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if _, err := SeedProfiles(store); err != nil {
		log.Fatalf("seed: %v", err)
	}

	cfg := ServerConfig{
		Secret: secret,
		Auth: authConfig{
			webClientID:     os.Getenv("GITHUB_CLIENT_ID"),
			webClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
			webRedirectURI:  envOr("GITHUB_REDIRECT_URI", "http://localhost:"+port+"/auth/github/callback"),
			cliClientID:     os.Getenv("GITHUB_CLI_CLIENT_ID"),
			cliClientSecret: os.Getenv("GITHUB_CLI_CLIENT_SECRET"),
			webAppURL:       os.Getenv("WEB_APP_URL"),
			adminUsernames:  parseAdminUsernames(os.Getenv("ADMIN_GITHUB_USERNAMES")),
		},
	}

	logStartupConfig(cfg)

	srv := NewServer(store, cfg)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s (db=%s)", port, dbPath)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseAdminUsernames(s string) map[string]bool {
	m := make(map[string]bool)
	for _, u := range strings.Split(s, ",") {
		u = strings.ToLower(strings.TrimSpace(u))
		if u != "" {
			m[u] = true
		}
	}
	return m
}

func logStartupConfig(cfg ServerConfig) {
	preview := func(s string) string {
		if s == "" {
			return "(unset)"
		}
		if len(s) <= 4 {
			return "(set, len=" + itoa(len(s)) + ")"
		}
		return s[:4] + "... (len=" + itoa(len(s)) + ")"
	}
	log.Printf("startup config:")
	log.Printf("  GITHUB_CLIENT_ID         = %s", preview(cfg.Auth.webClientID))
	log.Printf("  GITHUB_CLIENT_SECRET     = %s", preview(cfg.Auth.webClientSecret))
	log.Printf("  GITHUB_REDIRECT_URI      = %q", cfg.Auth.webRedirectURI)
	log.Printf("  GITHUB_CLI_CLIENT_ID     = %s", preview(cfg.Auth.cliClientID))
	log.Printf("  GITHUB_CLI_CLIENT_SECRET = %s", preview(cfg.Auth.cliClientSecret))
	log.Printf("  WEB_APP_URL              = %q", cfg.Auth.webAppURL)
	log.Printf("  ADMIN_GITHUB_USERNAMES   = %d entries", len(cfg.Auth.adminUsernames))
	log.Printf("  GRADER_TOKEN             = %v", os.Getenv("GRADER_TOKEN") != "")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
