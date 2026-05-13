# syntax=docker/dockerfile:1

# ---------- 1. Build SvelteKit static frontend ----------
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-fund --no-audit
COPY web/ ./
RUN npm run build

# ---------- 2. Build Go binary with embedded frontend ----------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Drop the placeholder build dir, copy the real one from stage 1.
RUN rm -rf internal/web/build
COPY --from=web /web/build internal/web/build
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/grail ./cmd/grail

# ---------- 3. Final image ----------
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/grail /grail
ENV DATA_DIR=/data \
    PORT=8080
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/grail"]
