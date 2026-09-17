# 前端镜像: 构建出静态产物, 交给 nginx 托管(并把 /api 反代到 api 服务)。
FROM node:22-alpine AS build

WORKDIR /app
# 用 pnpm(与仓库锁文件一致); corepack 负责按 packageManager 字段选版本
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY . .
# 构建里含 vue-tsc 类型检查: 类型不过 = 镜像构建失败, 不让坏包上生产
RUN pnpm build

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- http://127.0.0.1/ >/dev/null 2>&1 || exit 1
