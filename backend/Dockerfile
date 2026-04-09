FROM golang:1.26.2-trixie AS builder

WORKDIR /usr/src/app

COPY go.mod go.su[m] ./
RUN go mod download && go mod verify

COPY cmd ./cmd

RUN CGO_ENABLED=0 go build -o server ./cmd/scalable-forms/

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/src/app/server /server

CMD ["/server"]

