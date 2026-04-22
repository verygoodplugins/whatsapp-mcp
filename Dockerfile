FROM golang:1.24-bullseye AS builder
RUN apt-get update && apt-get install -y gcc libsqlite3-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o whatsapp-bridge .

FROM debian:bullseye-slim
RUN apt-get update && apt-get install -y libsqlite3-0 ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /app/whatsapp-bridge .
EXPOSE 8080
CMD ["./whatsapp-bridge"]
# force rebuild
