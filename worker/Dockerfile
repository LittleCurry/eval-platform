# 评测 worker 镜像: 队列消费者(检索 + 生成 + 判定 + 归因)。
#
# 与 api 分开打包的理由: 它们的依赖与生命周期完全不同 —— worker 要装 Python + httpx +
# qdrant-client, api 只要一个二进制; 而且扩缩容时通常只扩 worker(评测是 CPU/网络密集)。
FROM python:3.13-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /app
RUN useradd -m -u 10001 worker

# 先装依赖再拷代码: 依赖没变时这层命中缓存
COPY pyproject.toml README.md ./
COPY app ./app
RUN pip install --no-cache-dir -e .

# 把代码交还给运行用户, 并且**不信任宿主机带进来的权限**:
# 实测踩过 —— 仓库里有个文件是 0600(其他都是 0644), COPY 原样保留, 于是非 root 的
# worker 用户连"读自己代码"都被拒(PermissionError: /app/app/generation/__init__.py)。
# 镜像不该依赖开发机的 umask, 所以这里统一 chown + 补读/执行位。
RUN chown -R worker:worker /app && chmod -R a+rX /app

USER worker

# 常驻轮询领任务(并周期性接管心跳超时的僵尸任务)
CMD ["python", "-m", "app.cli.worker"]
