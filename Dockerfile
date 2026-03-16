FROM golang:1.21-alpine AS builder
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o whatsapp-bridge .

FROM alpine:latest
RUN apk add --no-cache sqlite-libs ca-certificates
WORKDIR /app
COPY --from=builder /app/whatsapp-bridge .
EXPOSE 8080
CMD ["./whatsapp-bridge"]
