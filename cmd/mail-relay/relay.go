package main

import (
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"

	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/notification/channels/smtp"
	"github.com/zitadel/zitadel/internal/notification/messages"
	"github.com/zitadel/zitadel/internal/notification/templates"
	"github.com/zitadel/zitadel/pkg/actions"
)

// payload is the body zitadel's HTTP email provider posts (types.serializableData).
type payload struct {
	ContextInfo struct {
		EventType             string `json:"eventType"`
		RecipientEmailAddress string `json:"recipientEmailAddress"`
	} `json:"contextInfo"`
	TemplateData templates.TemplateData `json:"templateData"`
	Args         map[string]any         `json:"args"`
}

type Sender struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	ReplyTo string `json:"replyTo,omitempty"`
}

type SendFunc func(cfg *smtp.Config, msg *messages.Email) error

// sendSMTP is zitadel's own SMTP channel, one connection per message as in the notifier.
func sendSMTP(cfg *smtp.Config, msg *messages.Email) error {
	channel, err := smtp.InitChannel(cfg)
	if err != nil {
		return err
	}
	return channel.HandleMessage(msg)
}

type Relay struct {
	SigningKey   string
	MailTemplate string
	SMTP         smtp.SMTP
	TLS          bool
	Senders      map[string]Sender
	Fallback     Sender
	Send         SendFunc
	LogContent   bool
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.LogContent {
		slog.Info("request", "signature", req.Header.Get(actions.SigningHeader), "body", string(body))
	}
	if r.SigningKey != "" {
		if err := actions.ValidatePayload(body, req.Header.Get(actions.SigningHeader), r.SigningKey); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	orgID, _ := p.Args["orgID"].(string)
	sender, ok := r.Senders[orgID]
	if !ok {
		sender = r.Fallback
	}

	// same steps as types.SendEmail + generateEmail for the SMTP provider
	rendered, err := templates.GetParsedTemplate(r.MailTemplate, p.TemplateData)
	if err != nil {
		slog.Error("render failed", "event", p.ContextInfo.EventType, "org", orgID, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	msg := &messages.Email{
		Recipients:          []string{p.ContextInfo.RecipientEmailAddress},
		Subject:             p.TemplateData.Subject,
		Content:             html.UnescapeString(rendered),
		TriggeringEventType: eventstore.EventType(p.ContextInfo.EventType),
	}
	if r.LogContent {
		slog.Info("rendered", "event", p.ContextInfo.EventType, "org", orgID, "subject", msg.Subject, "html", msg.Content)
	}

	cfg := &smtp.Config{SMTP: r.SMTP, Tls: r.TLS, From: sender.Address, FromName: sender.Name, ReplyToAddress: sender.ReplyTo}
	// non-2xx makes zitadel retry the notification
	if err := r.Send(cfg, msg); err != nil {
		slog.Error("send failed", "event", p.ContextInfo.EventType, "org", orgID, "err", err)
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	slog.Info("sent", "event", p.ContextInfo.EventType, "org", orgID, "from", sender.Address, "to", p.ContextInfo.RecipientEmailAddress)
	w.WriteHeader(http.StatusNoContent)
}
