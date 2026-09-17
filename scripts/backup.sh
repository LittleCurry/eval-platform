#!/usr/bin/env bash
# 备份数据库与向量库(M7-4)。
#
# 为什么必须一起备: chunk 正文与向量只在 Qdrant 里(PG 只存元信息, 见 process.md D17),
# 只备 PG 的话恢复后检索跑不了, 而重建索引要重新调 embedding(花钱且慢);
# 只备 Qdrant 的话连数据集和 run 记录都没有。
set -euo pipefail

BACKUP_DIR="${1:-backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
PG_DUMP="${BACKUP_DIR}/eval-${STAMP}.sql.gz"
QDRANT_DIR="${BACKUP_DIR}/qdrant-${STAMP}"

# 与 compose.yaml 的服务名/账号保持一致(容器内)
PG_USER="${POSTGRES_USER:-eval}"
PG_DB="${POSTGRES_DB:-eval_platform}"
QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"

mkdir -p "${BACKUP_DIR}" "${QDRANT_DIR}"

echo "== 1/2 PostgreSQL -> ${PG_DUMP}"
docker compose exec -T postgres pg_dump -U "${PG_USER}" -d "${PG_DB}" --clean --if-exists \
  | gzip > "${PG_DUMP}"
echo "   $(du -h "${PG_DUMP}" | cut -f1)"

echo "== 2/2 Qdrant 快照 -> ${QDRANT_DIR}"
collections="$(curl -sf "${QDRANT_URL}/collections" | python3 -c \
  "import json,sys; print(' '.join(c['name'] for c in json.load(sys.stdin)['result']['collections']))" 2>/dev/null || true)"
if [ -z "${collections}" ]; then
  echo "   (向量库里还没有集合, 跳过)"
else
  for name in ${collections}; do
    # Qdrant 的快照落在容器内的 /qdrant/snapshots, 拷出来才算备份完成
    file="$(curl -sf -X POST "${QDRANT_URL}/collections/${name}/snapshots" \
      | python3 -c "import json,sys; print(json.load(sys.stdin)['result']['name'])")"
    docker compose cp "qdrant:/qdrant/snapshots/${file}" "${QDRANT_DIR}/${file}" >/dev/null
    echo "   ${name}: ${file} ($(du -h "${QDRANT_DIR}/${file}" | cut -f1))"
  done
fi

echo
echo "✅ 备份完成: ${BACKUP_DIR}/"
echo "   恢复步骤见 docs/deploy.md 第三节(两份要配对使用)"
