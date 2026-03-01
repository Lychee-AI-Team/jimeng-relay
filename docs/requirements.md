# 即梦 4.0 API中转服务 - 需求规格说明书

> **版本**: v1.0.0  
> **更新日期**: 2026-02-26  
> **状态**: 已实现

---

## 目录

1. [项目概述](#1-项目概述)
2. [功能需求](#2-功能需求)
3. [服务端技术规范](#3-服务端技术规范)
4. [客户端技术规范](#4-客户端技术规范)
5. [安全设计](#5-安全设计)
6. [并发控制](#6-并发控制)
7. [错误处理](#7-错误处理)
8. [数据模型](#8-数据模型)
9. [部署配置](#9-部署配置)
10. [API接口规范](#10-api接口规范)
11. [测试策略](#11-测试策略)
12. [历史问题与经验教训](#12-历史问题与经验教训)
13. [开发规范](#13-开发规范)
14. [发布检查清单](#14-发布检查清单)

---

## 1. 项目概述

### 1.1 项目背景

即梦 4.0 图片 / 3.0 视频 API中转服务（Jimeng Relay）是一个高性能的API网关服务，位于客户端与火山引擎即梦 4.0 图片 / 3.0 视频 API之间，提供：

- **统一鉴权**：AWS SigV4签名算法验证客户端身份
- **审计追踪**：记录所有请求和响应的完整生命周期
- **并发控制**：双层限流机制保护上游服务
- **幂等支持**：防止重复提交导致的资源浪费

### 1.2 系统架构

```
┌─────────────┐      ┌─────────────────────┐      ┌─────────────────┐
│   Client    │ ──── │    Relay Server     │ ──── │   火山引擎API    │
│  (Go CLI)   │      │  (鉴权/限流/审计)    │      │   (即梦4.0)     │
└─────────────┘      └─────────────────────┘      └─────────────────┘
                            │
                            ▼
                     ┌─────────────┐
                     │  Database   │
                     │ SQLite/PG   │
                     └─────────────┘
```

### 1.3 技术栈

| 组件 | 技术选型 |
|------|---------|
| 服务端语言 | Go 1.25.0 |
| 客户端语言 | Go 1.25.0 |
| 数据库 | SQLite (开发) / PostgreSQL (生产) |
| 鉴权算法 | AWS SigV4 (HMAC-SHA256) |
| 加密算法 | AES-256-GCM |
| 密码哈希 | bcrypt |

### 1.4 能力边界

#### 已支持
- 即梦 4.0 图片 / 3.0 视频 异步任务提交 (`CVSync2AsyncSubmitTask`)
- 即梦 4.0 图片 / 3.0 视频 结果查询 (`CVSync2AsyncGetResult`)
- 文生图 (t2i) / 文生视频 (t2v, Video 3.0)
- 图生图 (i2i) / 图生视频 (i2v, Video 3.0)
- 图生图 (i2i，URL和本地文件)

#### 不支持
- 同步接口
- 其他火山引擎服务
- WebSocket长连接

---

## 2. 功能需求

### 2.1 服务端功能

#### 2.1.1 核心转发

| 功能 | 描述 | 优先级 |
|------|------|--------|
| Submit转发 | 接收客户端请求，转发至火山引擎 | P0 |
| GetResult转发 | 查询任务结果并返回 | P0 |
| 透明代理 | 保留原始请求体和响应体 | P0 |

#### 2.1.2 鉴权与安全

| 功能 | 描述 | 优先级 |
|------|------|--------|
| SigV4验证 | 验证客户端签名，防止伪造 | P0 |
| Key生命周期 | 创建/列表/吊销/轮换 | P0 |
| 密钥加密 | 数据库中API Key Secret加密存储 | P0 |
| 失败审计 | 审计失败时拒绝请求(Fail-Closed) | P0 |

#### 2.1.3 并发控制

| 功能 | 描述 | 优先级 |
|------|------|--------|
| Per-Key限流 | 每个Key同时只能有1个活跃请求 | P0 |
| 全局限流 | 限制转发到上游的总并发数 | P0 |
| FIFO队列 | 超出并发限制的请求进入队列 | P1 |
| Submit节流 | 两次submit之间的最小间隔 | P1 |

#### 2.1.4 可靠性

| 功能 | 描述 | 优先级 |
|------|------|--------|
| 幂等支持 | 基于Idempotency-Key的请求去重 | P1 |
| 审计日志 | 记录请求/响应的完整生命周期 | P0 |
| Panic恢复 | 单请求panic不影响整体服务 | P0 |
| 超时控制 | 服务端/上游请求超时配置 | P0 |

### 2.2 客户端功能

#### 2.2.1 核心命令

| 命令 | 描述 | 优先级 |
|------|------|--------|
| submit | 提交图片/视频生成任务 | P0 |
| query | 查询任务状态 (支持图片/视频) | P0 |
| wait | 等待任务完成 (支持图片/视频) | P0 |
| download | 下载生成的图片/视频 | P0 |

#### 2.2.2 生成模式

| 模式 | 描述 | 优先级 |
|------|------|--------|
| 文生图(t2i) | 根据文字描述生成图片 | P0 |
| 图生图(i2i) | 基于图片进行变换 | P0 |
| 文生视频(t2v) | 根据文字描述生成视频 | P0 |
| 图生视频(i2v) | 基于图片生成视频 | P0 |
| 图生图(i2i-URL) | 基于URL图片进行变换 | P0 |
| 图生图(i2i-File) | 基于本地文件进行变换 | P0 |

#### 2.2.3 输出控制

| 功能 | 描述 | 优先级 |
|------|------|--------|
| JSON格式 | 结构化输出便于解析 | P0 |
| 文件下载 | URL或base64回退下载 | P0 |
| 命名规则 | task_id前缀避免覆盖 | P1 |

---

## 3. 服务端技术规范

### 3.1 目录结构

```
server/
├── cmd/
│   └── server/
│       └── main.go              # 服务入口、路由注册
├── internal/
│   ├── config/
│   │   └── config.go            # 配置加载
│   ├── handler/
│   │   └── relay/
│   │       ├── submit.go        # Submit处理器
│   │       ├── get_result.go    # GetResult处理器
│   │       └── util.go          # 工具函数
│   ├── middleware/
│   │   ├── sigv4/
│   │   │   └── middleware.go    # SigV4鉴权中间件
│   │   └── observability/
│   │       ├── middleware.go    # 可观测性中间件
│   │       └── recover.go       # Panic恢复中间件
│   ├── relay/
│   │   └── upstream/
│   │       ├── client.go        # 上游客户端+并发控制
│   │       └── context.go       # Context工具
│   ├── service/
│   │   ├── apikey/
│   │   │   └── service.go       # API Key服务
│   │   ├── audit/
│   │   │   └── service.go       # 审计服务
│   │   ├── idempotency/
│   │   │   └── service.go       # 幂等服务
│   │   └── keymanager/
│   │       └── service.go       # Per-Key限流服务
│   ├── repository/
│   │   ├── interface.go         # 存储接口
│   │   ├── sqlite/
│   │   │   └── sqlite.go        # SQLite实现
│   │   └── postgres/
│   │       └── postgres.go      # PostgreSQL实现
│   ├── models/
│   │   ├── api_key.go           # API Key模型
│   │   ├── audit_event.go       # 审计事件模型
│   │   └── idempotency_record.go # 幂等记录模型
│   ├── secretcrypto/
│   │   └── cipher.go            # AES-256加密
│   ├── errors/
│   │   └── errors.go            # 错误定义
│   └── logging/
│       └── logger.go            # 日志工具
├── docs/
│   ├── concurrency-runbook.md   # 并发运维手册
│   ├── stability-hardening-lessons.md # 稳定性加固经验
│   └── release-checklist.md     # 发布检查清单
└── scripts/
    └── local_e2e_concurrency.go # 并发测试脚本
```

### 3.2 配置规范

#### 环境变量

| 变量名 | 必填 | 默认值 | 说明 |
|--------|------|--------|------|
| `VOLC_ACCESSKEY` | 是 | - | 火山引擎Access Key |
| `VOLC_SECRETKEY` | 是 | - | 火山引擎Secret Key |
| `VOLC_REGION` | 否 | `cn-north-1` | 火山引擎Region |
| `VOLC_HOST` | 否 | `visual.volcengineapi.com` | 即梦API域名 |
| `API_KEY_ENCRYPTION_KEY` | 是 | - | Base64编码的32字节密钥 |
| `SERVER_PORT` | 否 | `8080` | 服务监听端口 |
| `DATABASE_TYPE` | 否 | `sqlite` | 数据库类型 |
| `DATABASE_URL` | 否 | `./jimeng-relay.db` | 数据库连接串 |
| `VOLC_TIMEOUT` | 否 | `30s` | 上游请求超时 |
| `UPSTREAM_MAX_CONCURRENT` | 否 | `1` | 上游最大并发 |
| `UPSTREAM_MAX_QUEUE` | 否 | `100` | 排队队列大小 |
| `UPSTREAM_SUBMIT_MIN_INTERVAL` | 否 | `0s` | Submit最小间隔 |
| `PER_KEY_MAX_CONCURRENT` | 否 | `1` | 每Key最大并发 |
| `PER_KEY_MAX_QUEUE` | 否 | `0` | 每Key排队大小 (固定为0) |

#### 配置加载优先级

1. 命令行flag（最高）
2. 系统环境变量
3. `.env`文件

### 3.3 HTTP服务器配置

```go
// 必须配置的超时参数
srv := &http.Server{
    Addr:              ":" + cfg.ServerPort,
    Handler:           handler,
    ReadHeaderTimeout: 10 * time.Second,  // 防止Slowloris攻击
    ReadTimeout:       30 * time.Second,  // 请求体读取超时
    WriteTimeout:      60 * time.Second,  // 响应写入超时
    IdleTimeout:       120 * time.Second, // Keep-Alive空闲超时
    MaxHeaderBytes:    1 << 20,           // 1MB请求头限制
}
```

### 3.4 资源边界

| 资源 | 限制 | 说明 |
|------|------|------|
| 上游响应体 | 8MB | 防止OOM |
| Retry-After延迟 | 60s | 防止过度等待 |
| 请求体 | 通过`http.MaxBytesReader`限制 | 防止大请求攻击 |
| 请求体限制 | 20MiB | 防止大请求攻击 |
---

## 4. 客户端技术规范

### 4.1 目录结构

```
client/
├── main.go                      # 入口
├── cmd/
│   ├── root.go                  # 根命令
│   ├── submit.go                # Submit命令
│   ├── query.go                 # Query命令
│   ├── wait.go                  # Wait命令
│   └── download.go              # Download命令
├── internal/
│   ├── config/
│   │   ├── config.go            # 配置加载
│   │   └── credentials.go       # 凭证管理
│   ├── jimeng/
│   │   ├── client.go            # HTTP客户端
│   │   ├── submit.go            # Submit逻辑
│   │   ├── getresult.go         # GetResult解析
│   │   ├── wait.go              # 等待逻辑
│   │   ├── flow.go              # 流程编排
│   │   ├── validation.go        # 参数验证
│   │   └── retry.go             # 重试逻辑
│   ├── api/
│   │   └── matrix.go            # API矩阵
│   ├── output/
│   │   └── output.go            # 输出处理
│   ├── errors/
│   │   └── errors.go            # 错误定义
│   └── logging/
│       └── logger.go            # 日志工具
└── test/
    └── testutil.go              # 测试工具
```

### 4.2 命令参数规范

#### submit命令

```bash
jimeng submit \
  --prompt <描述文本>           # 必填：生成描述
  --resolution <WxH>            # 可选：分辨率，默认2048x2048
  --count <1-4>                 # 可选：生成数量，默认1
  --quality <速度优先|质量优先>  # 可选：生成质量，默认速度优先
  --image-url <URL>             # 可选：图生图URL输入
  --image-file <PATH>           # 可选：图生图文件输入
  --wait                        # 可选：等待完成
  --wait-timeout <duration>     # 可选：等待超时，默认5m
  --download-dir <PATH>         # 可选：下载目录
  --overwrite                   # 可选：覆盖已存在文件
  --format <text|json>          # 可选：输出格式，默认text
```

#### query命令

```bash
jimeng query \
  --task-id <ID>                # 必填：任务ID
  --format <text|json>          # 可选：输出格式
```

#### wait命令

```bash
jimeng wait \
  --task-id <ID>                # 必填：任务ID
  --interval <duration>         # 可选：轮询间隔，默认2s
  --wait-timeout <duration>     # 可选：等待超时，默认5m
  --format <text|json>          # 可选：输出格式
```

#### download命令

```bash
jimeng download \
  --task-id <ID>                # 必填：任务ID
  --dir <PATH>                  # 可选：下载目录，默认./outputs
  --overwrite                   # 可选：覆盖已存在文件
  --format <text|json>          # 可选：输出格式
```

### 4.3 输出文件命名规则

| 场景 | 命名格式 | 示例 |
|------|---------|------|
| URL下载 | `<task_id>-<原始文件名>` | `abc123-image.png` |
| Base64解码 | `<task_id>-image-<n>.png` | `abc123-image-1.png` |

### 4.4 错误解析规范

客户端必须优先解析relay返回的`error`对象：

```go
// 正确：优先检查error对象
if errObj := response["error"]; errObj != nil {
    // 解析错误码和消息
}

// 错误：直接读取业务字段（可能为空）
code := response["code"]  // 可能是0（默认值）
```

---

## 5. 安全设计

### 5.1 鉴权流程

```
Client                                    Server
  │                                         │
  │  1. 构造Canonical Request               │
  │  2. 生成String to Sign                  │
  │  3. 计算HMAC-SHA256签名                 │
  │────────────────────────────────────────>│
  │         Authorization Header            │
  │                                         │
  │                            4. 验证签名   │
  │                            5. 检查Key状态│
  │                            6. 记录审计   │
  │                                         │
  │<────────────────────────────────────────│
  │              Response                   │
```

### 5.2 SigV4签名规范

#### 签名算法

```
Authorization: HMAC-SHA256 
  Credential=<access_key>/<date>/<region>/<service>/request,
  SignedHeaders=content-type;host;x-content-sha256;x-date,
  Signature=<signature>
```

#### 签名步骤

1. **构造规范请求**
   ```
   HTTPMethod
   CanonicalURI
   CanonicalQueryString
   CanonicalHeaders
   SignedHeaders
   HashedPayload
   ```

2. **构造待签名字符串**
   ```
   HMAC-SHA256
   <X-Date>
   <Scope>
   HashedCanonicalRequest
   ```

3. **计算签名密钥**
   ```
   kDate = HMAC-SHA256("VOLC" + SecretKey, Date)
   kRegion = HMAC-SHA256(kDate, Region)
   kService = HMAC-SHA256(kRegion, Service)
   kSigning = HMAC-SHA256(kService, "request")
   ```

4. **计算最终签名**
   ```
   Signature = Hex(HMAC-SHA256(kSigning, StringToSign))
   ```

### 5.3 API Key加密存储

```go
// 加密算法：AES-256-GCM
// 密钥来源：API_KEY_ENCRYPTION_KEY (Base64编码的32字节)

type Cipher struct {
    key []byte  // 32字节
}

func (c *Cipher) Encrypt(plaintext string) (string, error) {
    block, _ := aes.NewCipher(c.key)
    gcm, _ := cipher.NewGCM(block)
    
    nonce := make([]byte, gcm.NonceSize())
    io.ReadFull(rand.Reader, nonce)
    
    ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
    return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (c *Cipher) Decrypt(ciphertext string) (string, error) {
    data, _ := base64.StdEncoding.DecodeString(ciphertext)
    
    block, _ := aes.NewCipher(c.key)
    gcm, _ := cipher.NewGCM(block)
    
    nonceSize := gcm.NonceSize()
    nonce, cipherData := data[:nonceSize], data[nonceSize:]
    
    plaintext, err := gcm.Open(nil, nonce, cipherData, nil)
    return string(plaintext), err
}
```

#### 5.3.1 密钥验证要求

`API_KEY_ENCRYPTION_KEY` 必须满足以下条件：
- 必须是 Base64 编码字符串
- 解码后必须正好是 32 字节 (256位)


### 5.4 Fail-Closed审计策略

```go
// 审计失败时必须拒绝请求
if err := h.audit.RecordRelayDownstream(ctx, call); err != nil {
    // 返回500错误，不继续处理
    writeRelayError(w, err, http.StatusInternalServerError)
    return
}
```

### 5.5 脱敏规范

| 字段 | 脱敏规则 |
|------|---------|
| Access Key | 保留前4位 + `...` |
| Secret Key | 完全隐藏 `***` |
| 请求体 | 不记录敏感字段 |

#### 5.5.1 安全警告

- **.env 加载风险**: 在可写运行时目录中加载 `.env` 文件存在被恶意篡改或泄露的风险。生产环境建议使用系统环境变量。
- **VOLC_HOST 覆盖风险**: 覆盖 `VOLC_HOST` 可能导致凭证暴露给非预期的中间节点。除非在受控的测试/代理环境下，否则不建议修改。


## 6. 并发控制

### 6.1 双层限流架构

```
                    ┌─────────────────────────────────────┐
                    │           Relay Server              │
                    │                                     │
Request ─────────>  │  ┌─────────────────────────────┐    │
                    │  │    Layer 1: Per-Key Limit   │    │
                    │  │    (KeyManager in-memory)   │    │
                    │  └──────────────┬──────────────┘    │
                    │                 │                   │
                    │                 ▼                   │
                    │  ┌─────────────────────────────┐    │
                    │  │    Layer 2: Global Limit    │    │
                    │  │    (Semaphore + FIFO Queue) │    │
                    │  └──────────────┬──────────────┘    │
                    │                 │                   │
                    └─────────────────┼───────────────────┘
                                      │
                                      ▼
                              ┌───────────────┐
                              │  Upstream API │
                              └───────────────┘
```

### 6.2 Per-Key限流实现

```go
// KeyManager: 内存中的Key状态管理
type Service struct {
    mu    sync.RWMutex
    keys  map[string]*keyState
}

type keyState struct {
    inUse    bool
    waiterCh chan struct{}
}

func (s *Service) AcquireKey(ctx context.Context, keyID string) (KeyHandle, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    state := s.getOrCreate(keyID)
    
    // 如果Key正在使用，检查是否可以排队
    if state.inUse {
        if len(state.waiterCh) >= s.maxQueue {
            return nil, ErrRateLimited
        }
        // 进入排队
        select {
        case state.waiterCh <- struct{}{}:
            state.inUse = true
            return &handle{state: state}, nil
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    
    state.inUse = true
    return &handle{state: state}, nil
}
```

### 6.3 全局并发控制实现

```go
// Client: 信号量 + FIFO队列
type Client struct {
    sem      chan struct{}      // 并发信号量
    waiters  []*queueWaiter     // FIFO等待队列
    maxQueue int                // 队列最大长度
}

func (c *Client) acquire(ctx context.Context) error {
    c.mu.Lock()
    
    // 尝试直接获取信号量
    select {
    case c.sem <- struct{}{}:
        c.mu.Unlock()
        return nil
    default:
    }
    
    // 队列已满，立即拒绝
    if len(c.waiters) >= c.maxQueue {
        c.mu.Unlock()
        return ErrQueueFull
    }
    
    // 进入队列等待
    w := &queueWaiter{ready: make(chan struct{})}
    c.waiters = append(c.waiters, w)
    c.mu.Unlock()
    
    select {
    case <-w.ready:
        return nil
    case <-ctx.Done():
        // 取消时必须补偿
        if !c.removeWaiter(w) {
            c.reassignCancelledWaiterSlot()
        }
        return ctx.Err()
    }
}

func (c *Client) release() {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    // 优先唤醒等待者（FIFO）
    if len(c.waiters) > 0 {
        w := c.waiters[0]
        c.waiters = c.waiters[1:]
        close(w.ready)
        return
    }
    
    // 无等待者，释放信号量
    <-c.sem
}
```

### 6.4 Submit节流控制

```go
func (c *Client) waitSubmitInterval(ctx context.Context) error {
    if c.submitMinInterval <= 0 {
        return nil
    }
    
    for {
        now := c.now().UTC()
        
        c.submitMu.Lock()
        if c.lastSubmitAt.IsZero() {
            c.lastSubmitAt = now
            c.submitMu.Unlock()
            return nil
        }
        
        next := c.lastSubmitAt.Add(c.submitMinInterval)
        if !now.Before(next) {
            c.lastSubmitAt = now
            c.submitMu.Unlock()
            return nil
        }
        
        waitFor := next.Sub(now)
        c.submitMu.Unlock()
        
        if err := c.sleep(ctx, waitFor); err != nil {
            return err
        }
    }
}
```

### 6.5 并发控制参数建议

| 场景 | UPSTREAM_MAX_CONCURRENT | UPSTREAM_MAX_QUEUE | UPSTREAM_SUBMIT_MIN_INTERVAL |
|------|------------------------|-------------------|------------------------------|
| 开发测试 | 1 | 10 | 0s |
| 低频使用 | 1 | 50 | 0s |
| 正常使用 | 2-3 | 100 | 1s |
| 高频使用 | 5+ | 200 | 2-3s |

---

## 7. 错误处理

### 7.1 错误码定义

```go
const (
    ErrUnknown           = "UNKNOWN"
    ErrValidationFailed  = "VALIDATION_FAILED"
    ErrAuthFailed        = "AUTH_FAILED"
    ErrKeyExpired        = "KEY_EXPIRED"
    ErrKeyRevoked        = "KEY_REVOKED"
    ErrRateLimited       = "RATE_LIMITED"
    ErrUpstreamFailed    = "UPSTREAM_FAILED"
    ErrDatabaseError     = "DATABASE_ERROR"
    ErrInternalError     = "INTERNAL_ERROR"
)
```

### 7.2 HTTP状态码映射

| 错误码 | HTTP状态码 | 说明 |
|--------|-----------|------|
| AUTH_FAILED | 401 | 鉴权失败 |
| KEY_EXPIRED | 401 | Key已过期 |
| KEY_REVOKED | 401 | Key已吊销 |
| RATE_LIMITED | 429 | 触发限流 |
| VALIDATION_FAILED | 400/405/413 | 参数验证失败 |
| UPSTREAM_FAILED | 502 | 上游错误 |
| DATABASE_ERROR | 500 | 数据库错误 |
| INTERNAL_ERROR | 500 | 内部错误 |

### 7.3 错误响应格式

```json
{
  "error": {
    "code": "RATE_LIMITED",
    "message": "api key is already in use",
    "request_id": "req_abc123"
  }
}
```

### 7.4 重试策略

```go
// 可重试状态码
func isRetriableStatus(resp *Response) bool {
    if resp.StatusCode == http.StatusTooManyRequests {
        return true
    }
    return resp.StatusCode >= 500 && resp.StatusCode <= 511
}

// 重试延迟计算
func retryDelay(retryAfter string, now time.Time) time.Duration {
    // 优先使用Retry-After头
    if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
        return min(seconds, maxRetryAfterDelay)  // 上限60s
    }
    
    // 指数退避
    return boundedBackoff(attempt)  // 200ms * 2^attempt, 上限2s
}
```

---

## 8. 数据模型

### 8.1 API Key

```sql
CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,           -- key_xxx格式
    access_key TEXT NOT NULL,      -- 加密存储
    secret_key TEXT NOT NULL,      -- 加密存储
    description TEXT,
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    revoked_at TIMESTAMP
);
```

### 8.2 审计事件

```sql
CREATE TABLE downstream_requests (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    api_key_id TEXT NOT NULL,
    action TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    query TEXT,
    client_ip TEXT,
    downstream_headers TEXT,       -- JSON
    downstream_body TEXT,          -- JSON
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE upstream_attempts (
    id TEXT PRIMARY KEY,
    downstream_request_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL,
    upstream_action TEXT NOT NULL,
    request_headers TEXT,          -- JSON
    request_body TEXT,             -- JSON
    response_status INTEGER,
    response_headers TEXT,         -- JSON
    response_body TEXT,            -- JSON
    latency_ms BIGINT,
    error TEXT,
    created_at TIMESTAMP NOT NULL,
    FOREIGN KEY (downstream_request_id) REFERENCES downstream_requests(id)
);
```

### 8.3 幂等记录

```sql
CREATE TABLE idempotency_records (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    api_key_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_status INTEGER NOT NULL,
    response_body TEXT NOT NULL,   -- JSON
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL
);
```

---

## 9. 部署配置

### 9.1 本地开发环境

```bash
# .env (server/)
VOLC_ACCESSKEY=your_access_key
VOLC_SECRETKEY=your_secret_key
API_KEY_ENCRYPTION_KEY=$(openssl rand -base64 32)
DATABASE_TYPE=sqlite
DATABASE_URL=./jimeng-relay.db
SERVER_PORT=8080
UPSTREAM_MAX_CONCURRENT=1
UPSTREAM_MAX_QUEUE=10

# .env (client/)
VOLC_ACCESSKEY=<relay生成的access_key>
VOLC_SECRETKEY=<relay生成的secret_key>
VOLC_HOST=localhost:8080
VOLC_SCHEME=http
VOLC_TIMEOUT=180s
```

### 9.2 生产环境

```bash
# 环境变量
VOLC_ACCESSKEY=<生产AK>
VOLC_SECRETKEY=<生产SK>
API_KEY_ENCRYPTION_KEY=<安全生成的32字节密钥>
DATABASE_TYPE=postgres
DATABASE_URL=postgres://user:password@host:5432/dbname?sslmode=require # 生产环境推荐使用 PostgreSQL，避免 SQLite 的并发限制
# 建议使用绝对路径配置 DATABASE_URL
SERVER_PORT=8080
UPSTREAM_MAX_CONCURRENT=3
UPSTREAM_MAX_QUEUE=100
UPSTREAM_SUBMIT_MIN_INTERVAL=1s
```

### 9.3 Docker部署

```dockerfile
# Dockerfile (server)
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o jimeng-server ./cmd/server

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/jimeng-server /usr/local/bin/
EXPOSE 8080
CMD ["jimeng-server", "serve"]
```

### 9.4 健康检查

```bash
# 服务健康检查
curl http://localhost:8080/health

# 期望响应
{"status": "ok"}
```

---

## 10. API接口规范

### 10.1 Submit接口

**请求**

```http
POST /v1/submit HTTP/1.1
Host: localhost:8080
Content-Type: application/json
Authorization: HMAC-SHA256 Credential=ak/...
X-Date: 20260226T120000Z
Idempotency-Key: unique-key-123

{
  "req_key": "jimeng_t2i_v40",
  "prompt": "一张产品海报，简洁背景",
  "width": 2048,
  "height": 2048,
  "use_sdk2": true,
  "return_url": true
}
```

**成功响应**

```json
{
  "code": 10000,
  "data": {
    "task_id": "task_abc123",
    "status": "Running"
  },
  "message": "success",
  "request_id": "req_xyz"
}
```

**错误响应**

```json
{
  "error": {
    "code": "RATE_LIMITED",
    "message": "api key is already in use",
    "request_id": "req_xyz"
  }
}
```

### 10.2 GetResult接口

**请求**

```http
POST /v1/get-result HTTP/1.1
Host: localhost:8080
Content-Type: application/json
Authorization: HMAC-SHA256 Credential=ak/...
X-Date: 20260226T120000Z

{
  "req_key": "jimeng_t2i_v40",
  "task_id": "task_abc123"
}
```

**成功响应（完成）**

```json
{
  "code": 10000,
  "data": {
    "task_id": "task_abc123",
    "status": "Done",
    "image_urls": ["https://..."],
    "binary_data_base64": ["..."]
  }
}
```

**成功响应（进行中）**

```json
{
  "code": 10000,
  "data": {
    "task_id": "task_abc123",
    "status": "Running"
  }
}
```

### 10.3 兼容路径

| 功能 | REST路径 | 兼容路径 |
|------|---------|---------|
| Submit | `/v1/submit` | `/?Action=CVSync2AsyncSubmitTask` |
| GetResult | `/v1/get-result` | `/?Action=CVSync2AsyncGetResult` |

---

## 11. 测试策略

### 11.1 单元测试

```bash
# 运行所有测试
go test ./...

# 竞态检测
go test -race ./...

# 覆盖率
go test -cover ./...
```

### 11.2 并发测试

```bash
# 本地E2E并发测试
go run ./scripts/local_e2e_concurrency.go
```

### 11.3 测试覆盖清单

| 模块 | 测试类型 | 文件 |
|------|---------|------|
| 配置加载 | 单元测试 | `config_test.go` |
| SigV4鉴权 | 单元测试 | `middleware_test.go` |
| Submit处理 | 单元测试 | `submit_test.go` |
| GetResult处理 | 单元测试 | `get_result_test.go` |
| 并发控制 | 单元+集成 | `client_test.go`, `client_internal_test.go` |
| KeyManager | 单元测试 | `service_test.go` |
| 加密解密 | 单元测试 | `cipher_test.go` |
| Panic恢复 | 单元测试 | `recover_test.go` |
| 客户端解析 | 单元+集成 | `getresult.go`, `integration_test.go` |

---

## 12. 历史问题与经验教训

### 12.1 事件时间线

#### 事件A: 同Key并发导致客户端误报

| 项目 | 内容 |
|------|------|
| **症状** | 两个客户端使用同一Key，第一个客户端失败：`BUSINESS_FAILED: code=0 status=0 message= request_id=` |
| **根因** | 客户端get-result解析器只从成功形态payload读取`code/status/message/request_id`。当relay返回`{"error": {...}}`时，这些字段为空并默认为零值 |
| **修复** | 1. 优先检测relay的`error`对象并映射错误码 2. 防御性回退：全空业务字段返回`DECODE_FAILED`而非假`BUSINESS_FAILED` |
| **文件** | `client/internal/jimeng/getresult.go` |
| **教训** | **永远优先解析错误对象，不要假设成功形态** |

#### 事件B: 队列handoff竞态

| 项目 | 内容 |
|------|------|
| **症状** | 潜在的waiter handoff窃取和信号量不一致 |
| **根因** | 排队waiter唤醒和令牌所有权协议允许边缘情况的重排序/窃取 |
| **修复** | 1. 直接handoff所有权语义 2. 取消补偿路径收紧 3. 确定性回归测试（A/B/C竞态+FIFO顺序） |
| **文件** | `server/internal/relay/upstream/client.go` |
| **教训** | **并发原语必须有明确的补偿路径，取消不是简单的移除** |

#### 事件C: 文档漂移

| 项目 | 内容 |
|------|------|
| **症状** | 发布检查清单默认值漂移，证据文件缺失/命名错误 |
| **根因** | 文档未与代码同步更新 |
| **修复** | 1. 对齐默认`UPSTREAM_MAX_CONCURRENT=1` 2. 添加必要的Wave3证据文件 3. CI/回放一致性检查 |
| **文件** | `server/docs/release-checklist.md` |
| **教训** | **文档是代码的一部分，必须版本控制并同步更新** |

### 12.2 加固变更清单

#### 运行时崩溃隔离

```go
// 顶层panic recovery中间件
func RecoverMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if recovered := recover(); recovered != nil {
                logPanic(recovered)
                writeError(w, http.StatusInternalServerError, "internal error")
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

#### 服务器超时加固

| 超时类型 | 值 | 目的 |
|---------|---|------|
| ReadHeaderTimeout | 10s | 防止Slowloris攻击 |
| ReadTimeout | 30s | 限制请求体读取时间 |
| WriteTimeout | 60s | 限制响应写入时间 |
| IdleTimeout | 120s | Keep-Alive空闲超时 |

#### 上游资源边界

| 资源 | 限制 | 常量 |
|------|------|------|
| 响应体 | 8MB | `maxUpstreamResponseBodyBytes` |
| Retry-After延迟 | 60s | `maxRetryAfterDelay` |

### 12.3 验证标准

每个变更必须通过：

```bash
go test ./... -count=1      # 所有测试
go test -race ./... -count=1 # 竞态检测
go build ./...               # 编译检查
go run ./scripts/local_e2e_concurrency.go  # E2E并发测试
```

---

## 13. 开发规范

> **详细规范文档**：
> - [开发规范 (coding-standards.md)](coding-standards.md) - 代码风格、错误处理、测试规范
> - [设计规范 (design-principles.md)](design-principles.md) - 架构设计、模块化、安全设计

### 13.1 代码规范

#### 错误处理

```go
// 正确：包装错误并添加上下文
if err != nil {
    return internalerrors.New(internalerrors.ErrUpstreamFailed, "submit request", err)
}

// 错误：丢失上下文
if err != nil {
    return err
}
```

#### 日志记录

```go
// 正确：结构化日志
logger.InfoContext(ctx, "request completed",
    "latency_ms", latencyMs,
    "status", statusCode,
)

// 错误：字符串拼接
log.Printf("request completed: latency=%dms, status=%d", latencyMs, statusCode)
```

#### 并发安全

```go
// 正确：明确的锁范围
c.mu.Lock()
waiters := c.waiters
c.waiters = nil
c.mu.Unlock()

// 错误：锁范围过大
c.mu.Lock()
// ... 大量代码
c.mu.Unlock()
```

### 13.2 Git提交规范

```
<type>(<scope>): <subject>

<body>

<footer>
```

**类型**：
- `feat`: 新功能
- `fix`: Bug修复
- `docs`: 文档更新
- `refactor`: 重构
- `test`: 测试
- `chore`: 构建/工具

**示例**：

```
fix(server): harden runtime guardrails

- Add top-level panic recovery middleware
- Configure explicit http.Server timeouts
- Add upper bound for upstream response body reads
- Cap Retry-After delay to prevent excessive sleep

Closes #123
```

### 13.3 代码审查清单

- [ ] 所有错误都有明确的错误码
- [ ] 敏感字段已脱敏
- [ ] 并发代码有适当的锁保护
- [ ] 有对应的单元测试
- [ ] 日志使用结构化格式
- [ ] 无硬编码的超时/限制值
- [ ] 文档已同步更新

### 13.4 新功能开发流程

1. **需求分析**：明确功能边界和验收标准
2. **技术设计**：确定架构和接口
3. **测试先行**：编写测试用例
4. **实现代码**：按规范编写代码
5. **代码审查**：通过PR审查
6. **集成测试**：E2E测试验证
7. **文档更新**：同步更新文档

---

## 14. 发布检查清单

### 14.1 发布前检查

```bash
# 1. 代码检查
go vet ./...
/tmp/go-bin/golangci-lint run

# 2. 测试
go test ./... -count=1
go test -race ./... -count=1

# 3. 编译
go build -o ./bin/jimeng-server ./cmd/server
go build -o ./bin/jimeng ./cmd/client

# 4. E2E测试
go run ./scripts/local_e2e_concurrency.go
```

### 14.2 配置检查

- [ ] `API_KEY_ENCRYPTION_KEY` 已生成（32字节Base64）
- [ ] `VOLC_ACCESSKEY` 和 `VOLC_SECRETKEY` 已配置
- [ ] `UPSTREAM_MAX_CONCURRENT` 与上游配额匹配
- [ ] `DATABASE_URL` 正确指向生产数据库
- [ ] SSL/TLS 配置正确（生产环境）

### 14.3 部署后验证

- [ ] 健康检查端点返回正常
- [ ] 创建测试API Key成功
- [ ] 使用测试Key提交任务成功
- [ ] 查询任务结果成功
- [ ] 下载生成图片成功
- [ ] 审计日志正常记录

### 14.4 回滚准备

- [ ] 保留上一个版本的二进制文件
- [ ] 数据库迁移可逆
- [ ] 配置回滚脚本就绪

---

## 附录

### A. 参考文档

- [火山引擎即梦AI图片生成4.0](https://www.volcengine.com/docs/85621/1817045)
- [AWS SigV4签名算法](https://docs.aws.amazon.com/general/latest/gr/signature-version-4.html)

### B. 生成加密密钥

```bash
# 生成32字节的Base64编码密钥
openssl rand -base64 32
```

### C. 常见问题排查

| 问题 | 排查步骤 |
|------|---------|
| 401 Unauthorized | 检查签名计算、Key状态 |
| 429 Rate Limited | 检查并发配置、队列状态 |
| 502 Bad Gateway | 检查上游服务状态、超时配置 |
| Panic崩溃 | 查看recover中间件日志 |

---
*文档版本: v1.0.0 | 最后更新: 2026-02-26*
