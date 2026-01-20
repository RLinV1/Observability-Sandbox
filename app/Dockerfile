FROM golang:1.24-alpine as builder
WORKDIR /app
COPY . .
RUN go mod tidy
RUN go build -o main .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/main .
RUN mkdir -p /var/log/app
EXPOSE 8080
CMD ["./main"]