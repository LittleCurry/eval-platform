# API 镜像: 多阶段构建 —— 构建阶段用 Go 工具链, 运行阶段只要一个静态二进制。
#
# 为什么用 alpine 而不是 scratch: 需要 ca-certificates(访问 LLM/Qdrant 的 HTTPS)
# 与 tzdata(TIMESTAMPTZ 显示本地时间), 这两样加起来不到 10MB, 换来的是能用
# `docker exec` 进去排查问题 —— 纯 scratch 镜像出问题时连个 sh 都没有。
FROM golang:1.25-alpine AS build

WORKDIR /src
# 先只拷依赖清单: 依赖没变时这一层命中缓存, 改业务代码不必重新下载模块
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0: 静态链接, 可以在 alpine 里直接跑
# -trimpath -ldflags "-s -w": 去掉构建机路径与符号表, 镜像更小也少泄露本机信息
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget

# 不用 root 跑: 容器被攻破时少一半危害
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/api /app/api
USER app

ENV PORT=8080
EXPOSE 8080
# 健康检查与 /healthz 一致(compose 的依赖等待也用它)
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/api"]
