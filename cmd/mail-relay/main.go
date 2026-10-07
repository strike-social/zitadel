// Command mail-relay is a target for zitadel's HTTP email provider. It renders
// the mail exactly like zitadel's SMTP provider does, but picks the sender per
// organization (args.orgID) before sending through SMTP.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/zitadel/zitadel/internal/notification/channels/smtp"
	"github.com/zitadel/zitadel/internal/notification/static"
)

func main() {
	senders := map[string]Sender{}
	if err := json.Unmarshal([]byte(os.Getenv("SENDERS")), &senders); err != nil {
		fatal("SENDERS must be JSON {\"<orgID>\":{\"name\":..,\"address\":..},\"default\":{..}}", err)
	}
	fallback, ok := senders["default"]
	if !ok {
		fatal("SENDERS needs a \"default\" entry", nil)
	}

	mailTemplate := static.DefaultMailTemplate
	if path := os.Getenv("MAIL_TEMPLATE_PATH"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			fatal("MAIL_TEMPLATE_PATH", err)
		}
		mailTemplate = string(b)
	}

	smtpCfg := smtp.SMTP{Host: os.Getenv("SMTP_HOST")}
	if user := os.Getenv("SMTP_USER"); user != "" {
		smtpCfg.PlainAuth = &smtp.PlainAuthConfig{User: user, Password: os.Getenv("SMTP_PASSWORD")}
	}

	relay := &Relay{
		SigningKey:   os.Getenv("SIGNING_KEY"),
		MailTemplate: mailTemplate,
		SMTP:         smtpCfg,
		TLS:          envBool("SMTP_TLS", true),
		Senders:      senders,
		Fallback:     fallback,
		Send:         sendSMTP,
		LogContent:   envBool("LOG_CONTENT", false),
	}
	if relay.SigningKey == "" {
		slog.Warn("SIGNING_KEY not set, requests are not verified")
	}

	mux := http.NewServeMux()
	mux.Handle("POST /send", relay)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	addr := ":" + envOr("PORT", "8080")
	slog.Info("listening", "addr", addr, "smtp", smtpCfg.Host, "orgs", len(senders)-1)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fatal("server", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return b
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
