# Static binary (pure-Go SQLite, no CGO) on distroless: no shell, runs as non-root (uid 65532).
FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/morgentraining .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/morgentraining /morgentraining
ENV MT_ADDR=:8080 MT_DB=/data/morgentraining.db MT_TZ=Europe/Berlin
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/morgentraining"]
CMD ["serve"]
