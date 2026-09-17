# 部署手册（M7-4）

面向"把这套系统装到一台内网机器上给同事用"的场景。全程只需要 Docker 与 Docker Compose，不需要在机器上装 Go / Python / Node。

> 本地开发（改代码）不看这份，看 `README.md` 的"本地开发"一节 —— 那边的 API/worker/web 都在宿主机跑，方便热重载。

---

## 一、三步启动

```bash
# ① 准备配置：复制模板，填两个必填项
cp .env.example .env
make jwt-secret          # 打印一个随机密钥，粘到 .env 的 JWT_SECRET=
# 再填 SILICONFLOW_API_KEY（embedding / generation / judge 都用它）
# 生产环境顺手改掉 POSTGRES_PASSWORD

# ② 起全栈（首次会构建镜像，3~5 分钟）
make deploy-up           # = docker compose up -d --build + 等 web 就绪

# ③ 打开 http://<这台机器的IP>:8080
#    首次打开是"创建管理员账号"：第一个账号自动成为管理员
```

`make deploy-up` 起六个服务，启动顺序由健康检查串起来：

```
postgres(healthy) ──┬─► migrate(跑完即退出) ──┬─► api(healthy) ──► web(对外 :8080)
                    └────────────────────────► worker
                    qdrant ───────────────────┘
```

**为什么迁移单独一个服务**：API 运行时不该有改表结构的权限；迁移失败就卡在 `migrate` 这一步，而不是让 API 带着半个 schema 起来。

常用命令：

```bash
make deploy-ps           # 服务状态
make deploy-logs         # 跟踪日志（docker compose logs -f --tail=100）
make deploy-down         # 停掉（保留数据卷）
docker compose up -d postgres qdrant   # 只想起依赖库（本地开发用）
```

---

## 二、端口与对外入口

| 服务 | 容器内 | 宿主机 | 说明 |
|------|--------|--------|------|
| web（nginx） | 80 | **`WEB_PORT`，默认 8080** | **唯一对外入口**，同源代理 `/api` |
| api | 8080 | 不暴露 | 需要直接调 API 时取消 compose 里 `api.ports` 的注释 |
| worker | — | 不暴露 | 队列消费者，无 HTTP |
| postgres | 5432 | `POSTGRES_HOST_PORT`，默认 5432 | 方便 psql 排查 |
| qdrant | 6333 / 6334 | `QDRANT_PORT` / `QDRANT_GRPC_PORT` | 向量库 |

**为什么只对外开 web**：同源就不需要 CORS、不需要在前端打包时写死后端地址（`VITE_API_BASE` 默认就是 `/api/v1`），而且同事只需要记一个地址。

> 与本地开发共用一台机器时注意：`make api-restart` 会占用 8080，和 `WEB_PORT` 撞车 —— 二选一，或把 `WEB_PORT` 改成 8090。

---

## 三、备份与恢复

### 备份

```bash
make backup                                  # 默认写进 backups/
make backup BACKUP_DIR=/data/eval-backups    # 指定目录（建议挂到外部盘）
```

产出两份东西，**必须一起备份**：

| 文件 | 内容 | 丢了会怎样 |
|------|------|-----------|
| `eval-<时间戳>.sql.gz` | PostgreSQL 全量（数据集、用例、run、指标、标注、金标、账号） | 实验记录全没了 |
| `qdrant-<时间戳>/` | Qdrant 快照（chunk 向量） | 检索跑不了，需要重建索引（要重新调 embedding，花钱且慢） |

### 恢复

```bash
# ① 数据库
gunzip -c backups/eval-20260917.sql.gz | \
  docker compose exec -T postgres psql -U eval -d eval_platform

# ② 向量库（快照文件先拷进容器，再让 Qdrant 恢复）
docker compose cp backups/qdrant-20260917/<快照文件> qdrant:/qdrant/snapshots/
docker compose exec qdrant ls /qdrant/snapshots
# 恢复接口按集合来：POST /collections/{集合名}/snapshots/recover
curl -X POST http://localhost:6333/collections/<集合名>/snapshots/recover \
  -H 'Content-Type: application/json' \
  -d '{"location": "file:///qdrant/snapshots/<快照文件>"}'
```

> **为什么向量库与数据库要分别备份**：chunk 正文与向量只在 Qdrant 里（PG 只存元信息，见 D17）。恢复时 PG 里的 `chunking_hash` 必须能对上 Qdrant 的集合名（`corpus{id}_{切分指纹前8位}`），所以两份要**同一次备份**配对使用。

---

## 四、升级

```bash
git pull
make backup              # 升级前先备份（迁移是不可逆的）
make deploy-up           # 重建镜像 + 跑迁移 + 滚动重启
```

- 迁移是**只进不退**的：镜像更新会先跑 `migrate up`；如果需要回滚到旧版本镜像，数据库 schema 不会自己回去（`make migrate-down` 只回退一个版本，且要手工确认）。
- 改过 `.env`（比如轮换 `JWT_SECRET`）后：`make deploy-up` 会重建 api 容器。**轮换 JWT_SECRET 会让所有人当前的登录立即失效**（这是无状态 token 的唯一"撤销"手段，见 D22）；只想让某人失效，用「用户管理」停用账号。

---

## 五、排障

| 现象 | 原因与处理 |
|------|-----------|
| `JWT_SECRET 必填` 启动即退出 | `.env` 没填。这是**故意的 fail-fast**：宁可起不来，也不要悄悄变成一个免登录的 API |
| `SILICONFLOW_API_KEY 必填` | 同上，worker 要用它调 embedding/generation/judge |
| `migrate` 容器退出码非 0 | 看 `docker compose logs migrate`；常见是数据库口令不一致（`.env` 改了但 PG 卷里还是旧口令 —— 旧卷要用旧口令，或重建卷并重新导入数据） |
| 页面能开但接口全 401 | 登录过期或 `JWT_SECRET` 被轮换过 —— 重新登录 |
| 提交了评测但一直"排队中" | worker 没在跑：`docker compose ps worker` / `make deploy-logs`。worker 起来后会自动领走队列里的任务 |
| 端口被占 | `WEB_PORT=8090 make deploy-up`（或先 `make api-stop` 停掉宿主机的 API） |
| 报告里"上下文正文"取不到 | Qdrant 集合名对不上：`collection` 由 `corpus_id + chunking_hash` 推出，重建过语料库就会换集合 |

---

## 六、安全清单（上内网前逐条过）

- [ ] `POSTGRES_PASSWORD` 已改（默认口令只适合本机）
- [ ] `JWT_SECRET` 是 `make jwt-secret` 生成的随机值，且**不在**任何文档/聊天里
- [ ] `.env` 没有进仓库（`.gitignore` 已含；`git check-ignore -v .env` 可确认）
- [ ] 管理员账号不是共享口令；给同事开的是 `editor`，只读的给 `viewer`
- [ ] `AUTH_DISABLED` 为 `false`（`true` 只用于本地演示，启动日志会大声警告）
- [ ] 如需外网访问：只暴露 `WEB_PORT`，前面再挂一层 HTTPS 反向代理（不要直接暴露 postgres/qdrant 端口）
