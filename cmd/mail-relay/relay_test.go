package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/zitadel/internal/notification/channels/smtp"
	"github.com/zitadel/zitadel/internal/notification/messages"
	"github.com/zitadel/zitadel/internal/notification/static"
	"github.com/zitadel/zitadel/internal/notification/templates"
	"github.com/zitadel/zitadel/pkg/actions"
)

const (
	reelvoomOrg = "376911057781788688"
	key         = "test-signing-key"
)

type sent struct {
	cfg *smtp.Config
	msg *messages.Email
}

func newRelay(err error) (*Relay, *[]sent) {
	var calls []sent
	return &Relay{
		SigningKey:   key,
		MailTemplate: static.DefaultMailTemplate,
		SMTP:         smtp.SMTP{Host: "smtp.example.com:465"},
		TLS:          true,
		Senders:      map[string]Sender{reelvoomOrg: {Name: "Reelvoom", Address: "support@reelvoom.com", ReplyTo: "support@reelvoom.com"}},
		Fallback:     Sender{Name: "Strike Social", Address: "noreply@strikesocial.com"},
		Send: func(cfg *smtp.Config, msg *messages.Email) error {
			calls = append(calls, sent{cfg, msg})
			return err
		},
	}, &calls
}

func templateData() templates.TemplateData {
	return templates.TemplateData{
		Subject:         "Initialize User",
		Greeting:        "Hello " + html.EscapeString(`Tom & "Jerry"`) + ",",
		Text:            "Use code 0M53RF or press {{.ButtonText}}.",
		URL:             "https://auth.example.com/ui/login/user/init?code=0M53RF&orgID=" + reelvoomOrg,
		ButtonText:      "Finish initialization",
		PrimaryColor:    "#5469d4",
		BackgroundColor: "#fafafa",
		FontColor:       "#000000",
		FontFamily:      templates.DefaultFontFamily,
	}
}

func body(orgID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"contextInfo":  map[string]any{"eventType": "user.human.initialization.code.added", "recipientEmailAddress": "a@example.com"},
		"templateData": templateData(),
		"args":         map[string]any{"orgID": orgID, "userID": "1"},
	})
	return b
}

func post(r http.Handler, b []byte, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(b))
	if header != "" {
		req.Header.Set(actions.SigningHeader, header)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func signed(b []byte) string { return actions.ComputeSignatureHeader(time.Now(), b, key) }

func TestSenderPerOrg(t *testing.T) {
	cases := []struct{ org, from, name, replyTo string }{
		{reelvoomOrg, "support@reelvoom.com", "Reelvoom", "support@reelvoom.com"},
		{"111", "noreply@strikesocial.com", "Strike Social", ""},
	}
	for _, c := range cases {
		relay, calls := newRelay(nil)
		b := body(c.org)
		if rec := post(relay, b, signed(b)); rec.Code != http.StatusNoContent {
			t.Fatalf("org %s: status %d %s", c.org, rec.Code, rec.Body)
		}
		if len(*calls) != 1 {
			t.Fatalf("org %s: %d sends", c.org, len(*calls))
		}
		cfg, msg := (*calls)[0].cfg, (*calls)[0].msg
		if cfg.From != c.from || cfg.FromName != c.name || cfg.ReplyToAddress != c.replyTo || !cfg.Tls || cfg.SMTP.Host != "smtp.example.com:465" {
			t.Errorf("org %s: smtp config %+v", c.org, cfg)
		}
		if msg.Subject != "Initialize User" || len(msg.Recipients) != 1 || msg.Recipients[0] != "a@example.com" {
			t.Errorf("org %s: message %+v", c.org, msg)
		}
	}
}

func TestContentIsZitadelSMTPRendering(t *testing.T) {
	relay, calls := newRelay(nil)
	b := body(reelvoomOrg)
	post(relay, b, signed(b))
	rendered, err := templates.GetParsedTemplate(static.DefaultMailTemplate, templateData())
	if err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0].msg.Content
	if got != html.UnescapeString(rendered) {
		t.Fatal("content differs from zitadel's SMTP rendering")
	}
	for _, want := range []string{`href="https://auth.example.com/ui/login/user/init?code=0M53RF&orgID=` + reelvoomOrg, "press Finish initialization", `Tom &amp; &#34;Jerry&#34;`} {
		if !strings.Contains(got, want) {
			t.Errorf("content missing %q", want)
		}
	}
}

func TestRejectsBadSignature(t *testing.T) {
	relay, calls := newRelay(nil)
	b := body(reelvoomOrg)
	for name, header := range map[string]string{
		"missing":  "",
		"tampered": signed(append([]byte(" "), b...)),
		"stale":    actions.ComputeSignatureHeader(time.Now().Add(-10*time.Minute), b, key),
	} {
		if rec := post(relay, b, header); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s signature: status %d", name, rec.Code)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("sent %d mails with bad signatures", len(*calls))
	}
}

func TestSendFailureIsRetriedByZitadel(t *testing.T) {
	relay, _ := newRelay(errors.New("smtp down"))
	b := body(reelvoomOrg)
	if rec := post(relay, b, signed(b)); rec.Code < 300 {
		t.Fatalf("status %d on send failure, zitadel would not retry", rec.Code)
	}
}
