# Builds the API as a static binary and runs it on a small Alpine image.
# Render (and any other container host) sets PORT; the server listens on it.
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20
# ca-certificates for TLS to Supabase/OpenAI/Clerk/WhatsApp; tzdata so agent
# timezones resolve.
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
USER app
ENV APP_ENV=production PORT=8080
EXPOSE 8080
CMD ["api"]
