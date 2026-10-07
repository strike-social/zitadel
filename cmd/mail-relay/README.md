# mail-relay

Target for Zitadel's HTTP email provider. Zitadel's email provider is instance-wide, so every org sends from the same address.
This relay receives Zitadel's notification webhook, renders the email exactly as Zitadel's SMTP provider would, and sends it
through SMTP with a sender chosen per organization.

```
Zitadel ──POST /send (signed JSON)──▶ mail-relay ──SMTP──▶ SES ──▶ user
                                      orgID → sender
```

Ticket: DATA-10977.

## Request flow

`relay.go` `Relay.ServeHTTP` handles `POST /send`:

1. **Read body.** Capped at 1 MiB. With `LOG_CONTENT=true` the raw body and signature header are logged.
2. **Verify signature.** `actions.ValidatePayload` checks `ZITADEL-Signature: t=<unix>,v1=<hmac-sha256>` against `SIGNING_KEY`
   with Zitadel's 300s tolerance. Failure → `401`. Skipped when `SIGNING_KEY` is empty (a warning is logged at startup).
3. **Parse payload.** Same shape Zitadel sends (`types.serializableData`):
   - `contextInfo.eventType`, `contextInfo.recipientEmailAddress`
   - `templateData`: subject, greeting, text, url, buttonText, colors, logo, font, footer. Already translated and filled with
     the org's message texts and branding by Zitadel.
   - `args`: user fields. Zitadel always sets `orgID` (the user's resource owner) and `userID`.
   Invalid JSON → `400`.
4. **Pick sender.** `SENDERS[args.orgID]`, else `SENDERS["default"]`.
5. **Render.** `templates.GetParsedTemplate(mailTemplate, templateData)` then `html.UnescapeString`. These are the same calls
   Zitadel's `types.SendEmail` and `generateEmail` make for the SMTP provider, so the HTML is identical. Render error → `500`.
6. **Send.** Builds a `messages.Email` and sends it with Zitadel's SMTP channel (`smtp.InitChannel` + `HandleMessage`), using a
   `smtp.Config` whose `From`, `FromName` and `ReplyToAddress` come from the chosen sender. One SMTP connection per email,
   as in Zitadel's notifier. Failure → `502`.
7. **Respond** `204`.

Zitadel treats any non-2xx as a failed notification and retries it (`Notifications.MaxAttempts`, default 3, within `MaxTtl`,
default 5m). The relay therefore only returns `204` after the SMTP server accepted the mail.

## Rendering details

Zitadel renders with Go `html/template`, twice, then unescapes HTML entities:

- Escaping is context-aware: URLs in `href`/`src` are percent-encoded and non http/https/mailto schemes become `#ZgotmplZ`;
  unsafe CSS values become `ZgotmplZ`; values inside CSS strings get CSS escapes.
- The second pass evaluates template syntax that appears in texts, e.g. `{{.ButtonText}}` inside a custom message text.
- HTML comments in the template are dropped, including Outlook `<!--[if mso]>` blocks.

Because the relay calls the same function on the same template, all of this matches Zitadel without reimplementation.

The template is Zitadel's default, `internal/notification/static/templates/template.html`, embedded through
`internal/notification/static/static.go`. Set `MAIL_TEMPLATE_PATH` to use another file. It must use the fields of
`templates.TemplateData` (`.Greeting`, `.Text`, `.URL`, `.ButtonText`, `.PrimaryColor`, `.LogoURL`, `.IncludeFooter`, ...).
A custom per-org MailTemplate stored in Zitadel is not fetched; only this one template is used for every org.

## Configuration

| Variable | Required | Description |
|---|---|---|
| `SENDERS` | yes | JSON map of orgID → sender, plus `default`. Example below. |
| `SIGNING_KEY` | yes in prod | `signingKey` returned when the HTTP email provider is created |
| `SMTP_HOST` | yes | `host:port`, e.g. `email-smtp.us-east-2.amazonaws.com:465` |
| `SMTP_USER`, `SMTP_PASSWORD` | for SES | SES SMTP credentials (plain/login auth) |
| `SMTP_TLS` | no, default `true` | `true` = implicit TLS (SES port 465). `false` = plain connection with no STARTTLS, only for local sinks |
| `MAIL_TEMPLATE_PATH` | no | Override the embedded template |
| `LOG_CONTENT` | no, default `false` | Log request body and rendered HTML. Contains codes and PII; debug only |
| `PORT` | no, default `8080` | Listen port |

```json
{
  "376911057781788688": {"name": "Reelvoom", "address": "support@reelvoom.com", "replyTo": "support@reelvoom.com"},
  "default": {"name": "Strike Social", "address": "noreply@strikesocial.com"}
}
```

Every `address` must be a verified SES identity (address or domain).

Endpoints: `POST /send`, `GET /healthz`.

## Zitadel setup

```bash
curl -X POST "$ZITADEL/admin/v1/email/http" -H "Authorization: Bearer $TOKEN" \
  -d '{"endpoint":"http://mail-relay.<ns>.svc.cluster.local:8080/send","description":"per-org sender relay"}'
# response contains id and signingKey → SIGNING_KEY
curl -X POST "$ZITADEL/admin/v1/email/<id>/_activate" -H "Authorization: Bearer $TOKEN" -d '{}'
```

Activating it replaces the SMTP provider for the whole instance, so every Zitadel email then depends on this service.
Run at least 2 replicas.

## Build and run

The relay imports `internal/notification/templates`, which pulls in `internal/i18n`. Its `init()` loads statik bundles that are
generated, not committed. Without them the binary panics at startup. Generate them before building or testing locally:

```bash
go install github.com/rakyll/statik@v0.1.8
go generate ./internal/api/ui/login/statik ./internal/notification/statik ./internal/statik
go test ./cmd/mail-relay
go run ./cmd/mail-relay
```

Docker (from the repository root; the Dockerfile runs the same generation):

```bash
docker build -f cmd/mail-relay/Dockerfile -t zitadel-mail-relay .
```

The image is distroless, runs as `nonroot`, and listens on 8080.

Run against SES:

```bash
docker run --rm -p 8080:8080 \
  -e SENDERS='{"<reelvoom-org-id>":{"name":"Reelvoom","address":"support@reelvoom.com","replyTo":"support@reelvoom.com"},"default":{"name":"Strike Social","address":"noreply@strikesocial.com"}}' \
  -e SIGNING_KEY='<signingKey>' \
  -e SMTP_HOST=email-smtp.us-east-2.amazonaws.com:465 \
  -e SMTP_USER='<ses-smtp-user>' \
  -e SMTP_PASSWORD='<ses-smtp-password>' \
  zitadel-mail-relay
```

Run locally against a test SMTP sink (no TLS, no auth), with content logging:

```bash
uv run --with aiosmtpd python -m aiosmtpd -n -l 0.0.0.0:1025 -c aiosmtpd.handlers.Mailbox ./maildir &

docker run --rm -p 8080:8080 \
  -e SENDERS='{"<reelvoom-org-id>":{"name":"Reelvoom","address":"support@reelvoom.com"},"default":{"name":"Strike Social","address":"noreply@strikesocial.com"}}' \
  -e SIGNING_KEY=local-key \
  -e SMTP_HOST=host.docker.internal:1025 \
  -e SMTP_TLS=false \
  -e LOG_CONTENT=true \
  zitadel-mail-relay
```

Send a signed test request:

```bash
BODY='{"contextInfo":{"eventType":"user.human.initialization.code.added","recipientEmailAddress":"a@example.com"},"templateData":{"subject":"Initialize User","greeting":"Hello,","text":"Please click below.","url":"https://example.com/init?code=0M53RF","buttonText":"Finish initialization"},"args":{"orgID":"<reelvoom-org-id>"}}'
T=$(date +%s)
SIG=$(printf '%s.%s' "$T" "$BODY" | openssl dgst -sha256 -hmac local-key -hex | awk '{print $NF}')
curl -i -X POST localhost:8080/send -H "ZITADEL-Signature: t=$T,v1=$SIG" -d "$BODY"   # expect 204
```

Delivered mails land in `./maildir/new/`.

To use a custom template, mount it and point `MAIL_TEMPLATE_PATH` at it:
`-v $PWD/reelvoom.html:/templates/reelvoom.html:ro -e MAIL_TEMPLATE_PATH=/templates/reelvoom.html`.

## Tests

`relay_test.go` replaces the SMTP send with a fake and checks:

- the sender (From, name, reply-to) is chosen by `args.orgID`, with fallback to `default`;
- the content equals Zitadel's own SMTP rendering of the same `templateData`;
- missing, tampered and stale signatures get `401` and nothing is sent;
- a send failure returns non-2xx so Zitadel retries.

## Known limitations

- One template for all orgs; Zitadel's per-org MailTemplate is not used.
- If SMTP accepted the mail but the `204` never reached Zitadel, the retry sends a duplicate.
- This code lives in an AGPL-3.0 fork of Zitadel and is covered by that license.
