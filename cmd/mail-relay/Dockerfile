# Builds only cmd/mail-relay. Build context is the repository root:
#   docker build -f cmd/mail-relay/Dockerfile -t zitadel-mail-relay .
FROM golang:1.25.8-alpine@sha256:8e02eb337d9e0ea459e041f1ee5eece41cbb61f1d83e7d883a3e2fb4862063fa AS build

WORKDIR /src
RUN go install github.com/rakyll/statik@v0.1.8

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend backend
COPY cmd cmd
COPY internal internal
COPY pkg pkg

# internal/i18n reads these bundles at init; same steps as nx generate-statik minus the login CSS
RUN go generate ./internal/api/ui/login/statik ./internal/notification/statik ./internal/statik

RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mail-relay ./cmd/mail-relay

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/mail-relay /mail-relay
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/mail-relay"]
