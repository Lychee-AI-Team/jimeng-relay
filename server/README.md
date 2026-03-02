# Jimeng Relay Server

Jimeng Relay Server 是一个高性能的即梦 4.0 API 中继服务，旨在为客户端提供统一的鉴权、审计、幂等性支持以及对上游即梦 API (图片 4.0 / 视频 3.0) 的透明转发。

## 能力边界

- **核心功能**：支持即梦 4.0 图片及 3.0 视频的任务提交 (`submit`) 和结果获取 (`get-result`)。
- **鉴权机制**：采用 AWS SigV4 签名算法进行客户端鉴权。
- **审计与监控**：记录所有下游请求与上游尝试，包含延迟、状态码及错误分类。
- **幂等性**：针对 `submit` 接口提供基于 `Idempotency-Key` 的幂等支持。
- **计费治理**：支持基于预设（Preset）的计费公式、预授权（Pre-Auth）与结算（Settle）机制。
- **管理端 API**：提供管理员引导、登录、API Key 管理、定价及预算设置等 HTTP 接口。
- **安全设计**：敏感字段（如 API Key Secret）在数据库中加密存储，审计失败采取 Fail-Closed 策略。
- **鉴权机制**：采用 AWS SigV4 签名算法进行客户端鉴权。
- **审计与监控**：记录所有下游请求与上游尝试，包含延迟、状态码及错误分类。
- **幂等性**：针对 `submit` 接口提供基于 `Idempotency-Key` 的幂等支持。
- **安全设计**：敏感字段（如 API Key Secret）在数据库中加密存储，审计失败采取 Fail-Closed 策略。

## 配置说明

服务通过环境变量或 `.env` 文件进行配置。

| 环境变量 | 必填 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `VOLC_ACCESSKEY` | 是 | - | 火山引擎 Access Key |
| `VOLC_SECRETKEY` | 是 | - | 火山引擎 Secret Key |
| `VOLC_REGION` | 否 | `cn-north-1` | 火山引擎 Region |
| `VOLC_HOST` | 否 | `visual.volcengineapi.com` | 即梦 API 域名 |
| `API_KEY_ENCRYPTION_KEY` | 是 | - | 用于加密 API Key Secret 的 Base64 编码密钥 (32字节) |
| `SERVER_PORT` | 否 | `8080` | 服务监听端口 |
| `DATABASE_TYPE` | 否 | `sqlite` | 数据库类型 (`sqlite` 或 `postgres`) |
| `DATABASE_URL` | 否 | `./jimeng-relay.db` | 数据库连接字符串 |
| `VOLC_TIMEOUT` | 否 | `30s` | 上游请求超时时间 |
| `UPSTREAM_MAX_CONCURRENT` | 否 | `1` | 上游并发请求上限 |
| `UPSTREAM_MAX_QUEUE` | 否 | `100` | 上游排队队列大小 |
| `UPSTREAM_SUBMIT_MIN_INTERVAL` | 否 | `0s` | 两次 submit 请求之间的最小间隔（建议按上游限流逐步调大） |
| `PER_KEY_MAX_CONCURRENT` | 否 | `1` | 单 Key 并发上限（当前为固定策略：只能为 1；其他值将启动失败） |
| `PER_KEY_MAX_QUEUE` | 否 | `0` | 单 Key 排队上限（当前为固定策略：只能为 0；其他值将启动失败） |

> **注意**：`API_KEY_ENCRYPTION_KEY` 必须是 32 字节原始密钥的 Base64 编码字符串。可以使用以下命令生成：
> `openssl rand -base64 32`

## 快速启动 (本地 SQLite)

1. **准备环境**：确保已安装 Go 1.25.0。
2. **配置变量**：在 `server/` 目录下创建 `.env` 文件并填写必要配置。
3. **启动服务**：
   ```bash
   cd server
   go run cmd/server/main.go serve
   ```
   服务启动后会自动创建 SQLite 数据库文件并执行迁移。

## Docker 部署

### 使用 Docker Compose (推荐)

1. **创建环境文件**：
   ```bash
   cp .env.example .env
   # 编辑 .env 填入必要配置
   ```

2. **启动服务**：
   ```bash
   docker compose up -d
   ```

3. **查看日志**：
   ```bash
   docker compose logs -f
   ```

4. **停止服务**：
   ```bash
   docker compose down
   ```

### 使用 Docker 直接运行

```bash
# 构建镜像
docker build -t jimeng-server .

# 运行容器
docker run -d \
  --name jimeng-server \
  -p 8080:8080 \
  -e VOLC_ACCESSKEY=your_access_key \
  -e VOLC_SECRETKEY=your_secret_key \
  -e API_KEY_ENCRYPTION_KEY=your_base64_key \
  -v jimeng-data:/data \
  jimeng-server

# 检查健康状态
curl http://localhost:8080/health
```

### 健康检查端点

| 端点 | 用途 | 认证 |
|:---|:---|:---|
| `GET /health` | Liveness probe (进程存活) | 不需要 |
| `GET /ready` | Readiness probe (服务就绪) | 不需要 |

响应示例：
```json
// GET /health
{"status": "ok"}

// GET /ready
{"status": "ok"}
```

> **详细部署文档**：参见 [docs/deployment.md](docs/deployment.md) 获取 Railway 部署和 PostgreSQL 配置指南。n## 命令行工具

`server` 二进制提供内置 CLI，用于服务启动和 API Key 生命周期管理。

```bash
# 编译
cd server
go build -o ./bin/jimeng-server ./cmd/server/main.go

# 查看帮助
./jimeng-server help
./jimeng-server key help
```

### API Key（必须通过 CLI 生成）

> `access_key/secret_key` 由 CLI 生成，服务端不再提供 `/v1/keys` HTTP 管理端点。

```bash
# 生成 key
./jimeng-server key create --description "prod-client-a" --expires-at 2026-12-31T23:59:59Z

# 列出 key
./jimeng-server key list

# 吊销 key
./jimeng-server key revoke --id key_xxx

# 轮换 key（默认 grace-period=5m）
./jimeng-server key rotate --id key_xxx --description "rotated" --grace-period 10m
### Railway 环境下创建 API Key

在 Railway 部署后，需要在 Railway 容器中创建 API Key（因为需要访问私有网络中的 PostgreSQL）：

**方式 1: 使用 Railway CLI SSH**
```bash
# 1. 登录 Railway
railway login

# 2. 链接到项目
railway link jimeng-relay

# 3. 选择服务
railway service jimeng-server

# 4. SSH 进入容器并创建 Key
railway ssh -- ./jimeng-server key create --description "prod-client" --expires-at 2026-12-31T23:59:59Z
```

**方式 2: 使用 Railway Dashboard Web Terminal**

1. 打开 Railway Dashboard
2. 进入 `jimeng-relay` 项目 → `jimeng-server` 服务
3. 点击右上角 **"Connect"** 或 **"Terminal"**
4. 在 Web Terminal 中执行：
   ```bash
   ./bin/jimeng-server key create --description "prod-client" --expires-at 2026-12-31T23:59:59Z
   ```

> **注意**：必须使用 `railway ssh` 或 Dashboard Terminal，不能使用 `railway run`，因为后者在本地执行，无法访问 Railway 私有网络中的 PostgreSQL。

**输出示例**：
```json
{
  "id": "key_xxxxxxxxxxxxx",
  "access_key": "AK_xxxxxxxxxxxxx",
  "secret_key": "SK_xxxxxxxxxxxxx",
  "description": "prod-client",
  "created_at": "2026-02-27T22:45:00Z",
  "expires_at": "2026-12-31T23:59:59Z"
}
```

> **重要**：请保存 `access_key` 和 `secret_key`，客户端需要使用它们进行 AWS SigV4 签名认证。
## 管理端 API (Admin API)

服务端提供了一套基于 Session Cookie 的管理端 API，用于管理员引导、登录及资源治理。所有管理端接口均以 `/admin` 开头。

### 1. 身份管理 (Auth)

| 功能 | 路径 | 方法 | 说明 |
| :--- | :--- | :--- | :--- |
| 管理员引导 | `/admin/bootstrap` | `POST` | 创建首个管理员账号（仅在无管理员时可用） |
| 登录 | `/admin/login` | `POST` | 验证邮箱密码并下发 `admin_session` Cookie |
| 登出 | `/admin/logout` | `POST` | 销毁当前 Session |
| 重置请求 | `/admin/reset-request` | `POST` | 发送密码重置邮件 |
| 重置密码 | `/admin/reset` | `POST` | 使用邮件中的 Token 重置密码 |

### 2. 资源治理 (Governance)

> **注意**：除 `bootstrap`、`login`、`reset-request` 和 `reset` 外，其余接口均需管理员登录。

| 功能 | 路径 | 方法 | 说明 |
| :--- | :--- | :--- | :--- |
| 创建 Key | `/admin/keys` | `POST` | 创建新的 API Key（返回 AK/SK） |
| 设置定价 | `/admin/pricing` | `POST` | 设置预设（Preset）的图片/视频单价 |
| 设置预算 | `/admin/budget` | `POST` | 设置特定 API Key 的可用额度 |
| 设置倍率 | `/admin/multiplier` | `POST` | 设置特定 API Key 的计费倍率（10000 = 1.0x） |

## 计费模型与定价 (Billing & Pricing)

### 1. 计费公式 (Pricing Formula)

服务端根据请求类型和预设单价计算预估成本：

- **图片生成**：`preset.image_cost * multiplier / 10000`
- **视频生成**：`preset.video_cost_per_second * duration_seconds * multiplier / 10000`

其中 `multiplier` 为整数，`10000` 代表 `1.0x`。例如，倍率为 `12000` 则代表 `1.2x`。

### 2. 计费流程 (Billing Lifecycle)

1. **预授权 (Pre-Auth)**：在 `submit` 阶段，根据预估成本从 API Key 的可用额度中扣除并转入“已预留（Reserved）”状态。如果余额不足，请求将返回 `400 Insufficient Budget`。
2. **结算 (Settle)**：当上游任务成功完成时，预留额度正式转为已消耗，并从总额度中扣除。
3. **释放 (Release)**：如果任务提交失败或上游返回错误，预留额度将原路退回至可用额度。

## 视频帧率规则 (Video Frames Rule)

视频生成的时长由 `frames` 参数决定，遵循以下规则：

- **公式**：`24 * n + 1` (n 为秒数)
- **允许范围**：`[121, 241]` (即 5s 或 10s)
- **默认值**：`121` (5s)
- **校验**：不符合公式或超出范围的 `frames` 将导致 `400 Validation Failed`。

## 兼容性声明 (Compatibility)

- **CLI 兼容**：原有的 `./jimeng-server key` 命令行工具依然保留，可继续用于 API Key 的生命周期管理。
- **客户端兼容**：Relay Server 的核心 API (`/v1/submit`, `/v1/get-result`) 保持不变，客户端无需修改签名逻辑或请求构造。
- **数据库迁移**：服务启动时会自动执行数据库迁移，以支持新的计费和管理表结构。
## 线上部署 (PostgreSQL)

1. **数据库准备**：准备一个 PostgreSQL 实例。
2. **设置环境变量**：
   ```bash
   DATABASE_TYPE=postgres
   DATABASE_URL=postgres://user:password@localhost:5432/dbname?sslmode=disable
   ```
3. **运行服务**：
   服务在连接到 PostgreSQL 时会自动执行初始化迁移。

## 客户端迁移说明

客户端从直接调用即梦 API 迁移到使用 Relay Server 仅需两步：

1. **切换 Base URL**：将请求域名指向 Relay Server 的地址（如 `http://localhost:8080`）。
2. **更新 AK/SK**：使用 `./jimeng-server key create` 生成的 API Key 对请求进行 SigV4 签名。
   - **Service**: `cv`
   - **Region**: 与服务端配置的 `VOLC_REGION` 一致

### 接口映射

| 功能 | Relay 路径 | 兼容路径 (Action 参数) |
| :--- | :--- | :--- |
| 提交任务 | `/v1/submit` | `/?Action=CVSync2AsyncSubmitTask` |
| 获取结果 | `/v1/get-result` | `/?Action=CVSync2AsyncGetResult` |

## 管理方式 (API Key)

API Key 管理仅通过 CLI 完成：`key create/list/revoke/rotate`。

## 开发与验证

```bash
# 运行所有测试
go test ./...

# 运行竞态检测
go test -race ./...

# 代码检查
go vet ./...

# 编译二进制文件
go build -o ./bin/jimeng-server ./cmd/server/main.go

# 命令行 lint（与 CI 一致）
/tmp/go-bin/golangci-lint run
```

## 安全与审计

- **脱敏**：日志和审计记录中会自动脱敏敏感字段。
- **Fail-Closed**：如果审计日志记录失败，服务将拒绝处理该请求并返回 500 错误，以确保合规性。
- **并发控制**：
  - **单 Key 限制**：每个 API Key 限制并发数为 1。同 Key 的第二个并发请求将立即触发 `429 RATE_LIMITED`。
  - **全局限制**：通过 `UPSTREAM_MAX_CONCURRENT` 限制总并发，超出部分进入 FIFO 队列。
  - **队列限制**：通过 `UPSTREAM_MAX_QUEUE` 限制排队长度，队满立即返回 `429 RATE_LIMITED`。
  - **范围说明**：上述限制目前为进程级行为，仅在单实例部署时提供严格保证。
- **节流控制**：通过 `UPSTREAM_SUBMIT_MIN_INTERVAL` 控制 submit 请求的最小间隔；当上游出现并发限流（如 50430）时，建议设置为 `1s`~`3s` 并观察。
- **错误语义**：
  - `401 Unauthorized`：鉴权失败、Key 已过期或已吊销。
  - `429 Too Many Requests`：触发单 Key 并发限制或全局队列已满。
  - `502 Bad Gateway`：上游服务返回错误或请求超时。
- **未覆盖能力**：当前版本仅支持即梦 4.0 图片及 3.0 视频的异步任务提交与查询，暂不支持同步接口或其他火山引擎服务。
## 传输策略与限制

### 1. 负载限制 (Payload Policy)
- **最大请求体**：`20MiB`。
  - **设计初衷**：支持视频生成中的“首尾帧”模式，允许同时嵌入两张本地图片（每张约 5MiB）及 JSON 封装开销。
  - **超出行为**：立即返回 `400 Bad Request`，错误码 `ErrValidationFailed`。
- **超时控制**：默认上游超时为 `30s` (由 `VOLC_TIMEOUT` 控制)。对于排队中的请求，客户端应适当调大超时时间。

### 2. 行为等价性 (Parity Constraints)
- **请求透传**：Relay 保证下游请求的 Body 原样转发至上游，不进行字段删减或重组。
- **Header 映射**：透传 `Content-Type` 和 `Accept`。同时保留并透传 `X-Request-Id` 以实现全链路追踪。
- **语义一致性**：Relay 仅作为鉴权与并发控制层，不改变即梦 API 的业务逻辑语义。直接调用与通过 Relay 调用在请求构造上应完全一致。

## 故障排查 (50400 Triage)

当接口返回 `50400` (Business Failed) 时，通常意味着请求已到达上游但业务逻辑校验未通过。请按以下步骤排查：

1. **检查 Entitlement (权益)**：确认当前使用的火山引擎 AK/SK 是否拥有对应模型（如即梦 4.0）的调用权限。
2. **验证 Scope (范围)**：确认请求的 `Action` 与 `Service` 是否匹配。Relay 仅支持 `cv` 服务。
3. **核对 ReqKey**：视频生成对不同预设（Preset）有严格的 `req_key` 要求，请参考客户端文档中的 API 矩阵。
4. **查看诊断字段**：Relay 返回的错误消息中包含完整的诊断上下文（Host, Region, Action, RequestID），请将其提供给技术支持。
5. **资源可用性**：检查图片 URL 是否可公开访问，或 Base64 编码是否完整。
