# LMBY 多阶段构建
#
#   1. 用 Node 构建前端 → web/dist
#   2. 用 Go 把前端 embed 进二进制
#   3. 运行镜像：Debian slim + ffmpeg + Intel 驱动
#
# 说明：运行镜像刻意选了 debian-slim 而不是 distroless/scratch ——
# 媒体服务器离不开 ffmpeg 与显卡驱动，静态镜像在这里没有价值。

# ---------------------------------------------------------------- 前端
FROM node:24-bookworm-slim AS webapp
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------- 后端
FROM golang:1.27-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用上面构建好的前端覆盖占位产物
COPY --from=webapp /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X github.com/hakureiyuyuko/lmby/internal/version.Version=${VERSION} -X github.com/hakureiyuyuko/lmby/internal/version.Commit=${COMMIT}" \
      -o /out/lmby ./cmd/lmby

# ---------------------------------------------------------------- 运行
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ffmpeg \
      ca-certificates \
      tzdata \
      intel-media-va-driver-non-free \
      libvpl2 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/lmby /usr/local/bin/lmby
COPY deploy/lmby.example.toml /etc/lmby/config.toml.example

ENV LMBY_LISTEN=":8099" \
    LMBY_DATA_DIR="/var/lib/lmby"

VOLUME /var/lib/lmby
EXPOSE 8099

ENTRYPOINT ["/usr/local/bin/lmby"]
CMD ["serve", "--config", "/etc/lmby/config.toml"]
