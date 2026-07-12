FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

FROM alpine:3.21
COPY --from=build /server /usr/local/bin/server
EXPOSE 8080 9090
ENTRYPOINT ["server"]
