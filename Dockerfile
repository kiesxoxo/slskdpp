# syntax=docker/dockerfile:1

# The runtime image. The default has no shell, so hook commands cannot
# run; build with --build-arg RUNTIME=alpine:3.20 to use them.
ARG RUNTIME=gcr.io/distroless/static-debian12:nonroot

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26.10-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/slskdpp ./cmd/slskdpp

FROM ${RUNTIME}
COPY --from=build /out/slskdpp /usr/local/bin/slskdpp
# Settings come from /config/slskdpp.yaml if it exists, and from
# SLSKDPP_* variables. State, downloads and partial files live in /data.
ENV SLSKDPP_DATA_DIR=/data \
    SLSKDPP_WEB_LISTEN=:5030
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
# Web UI, and the Soulseek listen port (forward it on your router).
EXPOSE 5030 2234
ENTRYPOINT ["/usr/local/bin/slskdpp", "-config", "/config/slskdpp.yaml"]
