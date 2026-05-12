FROM golang:1.26-alpine AS build

RUN apk add --no-cache build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/whatsapp-bridge .

FROM alpine:3.23

RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/whatsapp-bridge /app/whatsapp-bridge

EXPOSE 8080
CMD ["/app/whatsapp-bridge"]
