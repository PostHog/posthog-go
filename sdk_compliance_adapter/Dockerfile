FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy the SDK source
COPY go.mod go.sum ./
COPY *.go ./

# Copy adapter
COPY sdk_compliance_adapter/main.go /app/adapter/

# Download dependencies
RUN go mod download

# Build adapter
RUN cd /app/adapter && go build -o /app/adapter-server main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/adapter-server /app/adapter-server

EXPOSE 8080

CMD ["/app/adapter-server"]
