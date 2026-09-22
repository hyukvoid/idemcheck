FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./examples/safe-order-api

FROM alpine:3.20
COPY --from=build /out/app /app
EXPOSE 8081
ENTRYPOINT ["/app"]
