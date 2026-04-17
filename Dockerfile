FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/server /app/server
RUN mkdir -p /data && chown -R app:app /data
USER app

ENV PORT=8080
ENV DB_PATH=/data/profiles.db
EXPOSE 8080
CMD ["/app/server"]
