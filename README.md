<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25.0-00ADD8?style=flat&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/License-MIT-green" alt="License">
  <img src="https://img.shields.io/badge/API-即梦4.0-blueviolet" alt="API">
</p>

<h1 align="center">🎬 即梦 4.0 API 中转服务</h1>

<p align="center">
  <b>Jimeng Relay</b> — 高性能 AI 图片/视频生成 API 网关<br>
  <sub>统一鉴权 · 审计追踪 · 并发控制 · 幂等支持</sub>
</p>

<p align="center">
  <a href="https://railway.app/template/jimeng-relay" target="_blank">
    <img src="https://railway.app/button.svg" alt="Deploy on Railway">
  </a>
</p>

---

## 📖 项目简介

即梦 4.0 API 中转服务是一个位于客户端与火山引擎即梦 4.0 API 之间的高性能网关服务，为 AI 图像与视频生成场景提供企业级的可靠性保障。

```
┌─────────────┐      ┌─────────────────────┐      ┌─────────────────┐
│   Client    │ ──── │    Relay Server     │ ──── │   火山引擎 API   │
│  (Go CLI)   │      │  (鉴权/限流/审计)    │      │   (即梦 4.0)    │
└─────────────┘      └─────────────────────┘      └─────────────────┘
                            │
                            ▼
                     ┌─────────────┐
                     │  Database   │
                     │ SQLite / PG │
                     └─────────────┘
```

### ✨ 核心特性

| 特性 | 描述 |
|:---:|:---|
| 🔐 **统一鉴权** | AWS SigV4 签名算法验证客户端身份 |
| 📝 **审计追踪** | 完整记录请求/响应生命周期 |
| 🚦 **并发控制** | 双层限流（Per-Key + 全局队列）保护上游 |
| 🔄 **幂等支持** | 基于 `Idempotency-Key` 防止重复提交 |
| 🛡️ **安全存储** | AES-256-GCM 加密敏感字段 |

### 🎯 能力边界

**支持**
- 文生图 (Text-to-Image) / 文生视频 (Video 3.0)
- 图生图 (Image-to-Image) / 图生视频 (Video 3.0)
- 图生图 (Image-to-Image，支持 URL 和本地文件)
- 异步任务提交与结果查询

**不支持**
- 同步接口
- 其他火山引擎服务
- WebSocket 长连接

---

## 🚀 快速开始

### 1. 服务端启动

```bash
cd server

# 配置环境变量
cp .env.example .env
# 编辑 .env，填入火山引擎 AK/SK 和加密密钥

# 启动服务
go run cmd/server/main.go serve
```

### 2. 生成 API Key

```bash
cd server
go build -o ./bin/jimeng-server ./cmd/server/main.go

# 创建 API Key
./bin/jimeng-server key create --description "my-client" --expires-at 2026-12-31T23:59:59Z
```

### 3. 客户端使用

```bash
cd client
go build -o ./bin/jimeng .

# 文生图
./bin/jimeng submit \
  --prompt "一张赛博朋克风格的城市夜景" \
  --resolution 2048x2048 \
  --format json

# 图生图（本地文件）
./bin/jimeng submit \
  --prompt "将这张图片转成水彩画风格" \
  --image-file ./input.png \
  --format json
```

# 文生视频
./bin/jimeng video submit \
  --preset t2v-720 \
  --prompt "一只在森林中奔跑的小狗" \
  --format json
```

---

## 📁 项目结构

```
jimeng-relay/
├── server/                 # 服务端
│   ├── cmd/               # 入口命令
│   ├── internal/          # 内部实现
│   │   ├── handler/       # HTTP 处理器
│   │   ├── middleware/    # 中间件（鉴权、限流）
│   │   ├── service/       # 业务逻辑
│   │   └── repository/    # 数据访问层
│   └── docs/              # 服务端文档
├── client/                 # 客户端 CLI
│   ├── cmd/               # CLI 命令
│   ├── internal/          # 内部实现
│   │   └── jimeng/        # API 调用封装
│   └── docs/              # 客户端文档
└── docs/                   # 项目级文档
    └── requirements.md    # 完整需求规格说明书
```

---

## 📚 文档导航

### 核心文档

| 文档 | 说明 |
|:---|:---|
| [📋 需求规格说明书](docs/requirements.md) | 完整的功能需求、技术规范、API 定义 |
| [📝 开发规范](docs/coding-standards.md) | 代码风格、错误处理、测试规范 |
| [📐 设计规范](docs/design-principles.md) | 架构设计、模块化、安全设计 |
| [🖥️ 服务端 README](server/README.md) | 服务端配置、部署、Key 管理 |
| [💻 客户端 README](client/README.md) | 客户端安装、命令使用指南 |
### 服务端专题文档

| 文档 | 说明 |
|:---|:---|
| [🏃 并发运维手册](server/docs/concurrency-runbook.md) | 限流机制、队列配置、故障排查 |
| [🛡️ 稳定性经验教训](server/docs/stability-hardening-lessons.md) | 历史问题与解决方案 |
| [✅ 发布检查清单](server/docs/release-checklist.md) | 上线前必检项目 |
| [⚡ 性能优化](server/docs/perf.md) | 性能调优指南 |

### 客户端专题文档

| 文档 | 说明 |
|:---|:---|
| [📊 API 矩阵](client/docs/api-matrix.md) | 参数与响应格式对照表 |

---

## ⚙️ 技术栈

| 组件 | 选型 |
|:---|:---|
| 服务端语言 | Go 1.25.0 |
| 客户端语言 | Go 1.25.0 |
| 数据库 | SQLite (开发) / PostgreSQL (生产) |
| 鉴权算法 | AWS SigV4 (HMAC-SHA256) |
| 加密算法 | AES-256-GCM |
| 密码哈希 | bcrypt |

---

## 🔧 配置参考

### 服务端核心配置

| 环境变量 | 必填 | 默认值 | 说明 |
|:---|:---:|:---|:---|
| `VOLC_ACCESSKEY` | ✅ | - | 火山引擎 Access Key |
| `VOLC_SECRETKEY` | ✅ | - | 火山引擎 Secret Key |
| `API_KEY_ENCRYPTION_KEY` | ✅ | - | 32字节密钥的 Base64 编码 |
| `SERVER_PORT` | | `8080` | 服务监听端口 |
| `DATABASE_TYPE` | | `sqlite` | 数据库类型 |
| `UPSTREAM_MAX_CONCURRENT` | | `1` | 上游并发上限 |
| `UPSTREAM_MAX_QUEUE` | | `100` | 排队队列大小 |

### 客户端核心配置

| 环境变量 | 必填 | 默认值 | 说明 |
|:---|:---:|:---|:---|
| `VOLC_ACCESSKEY` | ✅ | - | API Key Access Key |
| `VOLC_SECRETKEY` | ✅ | - | API Key Secret Key |
| `VOLC_HOST` | | `visual.volcengineapi.com` | API 地址 |
| `VOLC_TIMEOUT` | | `30s` | 请求超时 |

---

## 🛡️ 安全设计

- **密钥加密存储** — API Key Secret 使用 AES-256-GCM 加密
- **签名验证** — AWS SigV4 签名防止请求伪造
- **敏感字段脱敏** — 日志和审计记录自动脱敏
- **Fail-Closed 策略** — 审计失败时拒绝请求，确保合规

---

## 🧪 开发验证

```bash
# 服务端
cd server && go test -race ./...

# 客户端
cd client && go test -race ./...

# 构建
cd server && go build -o ./bin/jimeng-server ./cmd/server/main.go
cd client && go build -o ./bin/jimeng .
```

---

## 📄 License

MIT License

---

<p align="center">
  <sub>Built with ❤️ for AI Image & Video Generation</sub>
</p>
