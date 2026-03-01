# Jimeng CLI Client

`jimeng` 是即梦 AI 图片 4.0 / 视频 3.0 的 Go 命令行客户端，支持：

- 文生图（t2i）/ 文生视频（t2v）
- 图生图（i2i）/ 图生视频（i2v）
- 图生图（i2i，支持 URL 和本地文件）
- 异步任务查询与等待
- 结果下载（URL 或 base64 回退）

## 1. 安装与构建

在 `client/` 目录下执行：

```bash
go build -o ./bin/jimeng .
./bin/jimeng --help
```

如果希望全局使用：

```bash
cp ./bin/jimeng /usr/local/bin/jimeng
```

## 2. 配置方式

支持三种来源，配置优先级如下：

1. 命令行 flag（最高）
2. 系统环境变量 (Environment Variables)
3. `.env` 文件（`client/.env`）
4. 默认值 (Default)

### 环境变量

| 变量名 | 说明 | 默认值 |
| --- | --- | --- |
| `VOLC_ACCESSKEY` | 火山引擎 AK（必填） | - |
| `VOLC_SECRETKEY` | 火山引擎 SK（必填） | - |
| `VOLC_REGION` | 区域 | `cn-north-1` |
| `VOLC_HOST` | API Host | `visual.volcengineapi.com` |
| `VOLC_SCHEME` | 协议 (http/https) | `https` |
| `VOLC_TIMEOUT` | 请求超时 | `30s` |

### 本地测试 Relay Server

如需连接本地 Relay Server 进行测试，请在 `.env` 中配置：

```bash
VOLC_SCHEME=http
VOLC_HOST=localhost:8080
VOLC_TIMEOUT=180s
```

说明：当服务端开启排队/节流时，客户端请求可能会在队列中等待，`VOLC_TIMEOUT` 建议大于服务端最大排队等待时间。

### `.env` 使用

```bash
cp .env.example .env
# 编辑 .env 填入你的 AK/SK

set -a
source .env
set +a
```

## 3. submit 命令（核心）

### 3.1 文生图

```bash
jimeng submit \
  --prompt "一张产品海报，简洁背景，高级质感" \
  --resolution 2048x2048 \
  --count 1 \
  --quality 速度优先 \
  --format json
```

### 3.2 图生图（URL 输入）

```bash
jimeng submit \
  --prompt "保留主体，改成未来科技风" \
  --image-url "https://example.com/input.png" \
  --resolution 2048x2048 \
  --quality 质量优先 \
  --format json
```

### 3.3 图生图（本地文件输入）

```bash
jimeng submit \
  --prompt "保留主体，改成未来科技风" \
  --image-file "./input.png" \
  --resolution 2048x2048 \
  --quality 质量优先 \
  --format json
```

### 3.4 一步式提交 + 等待 + 下载

```bash
jimeng submit \
  --prompt "一张产品海报，简洁背景，高级质感" \
  --resolution 2048x2048 \
  --count 3 \
  --quality 质量优先 \
  --wait \
  --wait-timeout 5m \
  --download-dir ./outputs \
  --overwrite \
  --format json
```

## 4. 新增参数说明

- `--resolution <WxH>`：分辨率，默认 `2048x2048`
- `--count <1-4>`：一次生成张数，默认 `1`
- `--quality`：`速度优先|质量优先`（也支持 `speed|quality`），默认 `速度优先`
- `--image-url`：图生图 URL 输入（可重复）
- `--image-file`：图生图本地文件输入（可重复，自动转 base64）

注意：

- `--image-url` 和 `--image-file` 互斥，不能同时使用。
- `--width/--height` 会覆盖 `--resolution`。

## 5. query / wait / download

### query

```bash
jimeng query --task-id <task_id> --format json
```

### wait

```bash
jimeng wait --task-id <task_id> --interval 2s --wait-timeout 5m --format json
```

### download

```bash
jimeng download --task-id <task_id> --dir ./outputs --overwrite --format json
```

## 6. video 命令（视频生成）

### 6.1 视频预设 (Presets)

| 预设名 | 说明 | 对应 ReqKey |
| :--- | :--- | :--- |
| `t2v-720` | 文生视频 720p | `jimeng_t2v_v30` |
| `t2v-1080` | 文生视频 1080p | `jimeng_t2v_v30_1080p` |
| `t2v-pro` | 文生视频 3.0 Pro | `jimeng_ti2v_v30_pro` |
| `i2v-first-720` | 图生视频 (首帧) 720p | `jimeng_i2v_first_v30` |
| `i2v-first` | 图生视频 (首帧) 1080p | `jimeng_i2v_first_v30_1080` |
| `i2v-first-tail-720` | 图生视频 (首尾帧) 720p | `jimeng_i2v_first_tail_v30` |
| `i2v-first-tail` | 图生视频 (首尾帧) 1080p | `jimeng_i2v_first_tail_v30_1080` |
| `i2v-first-pro` | 图生视频 3.0 Pro (首帧) | `jimeng_ti2v_v30_pro` |
| `i2v-recamera` | 图生视频 (运镜) | `jimeng_i2v_recamera_v30` |

### 6.2 视频变体说明

| 变体 | 输入要求 | 特殊参数 |
|:---|:---|:---|
| t2v | 仅提示词 | --frames, --aspect-ratio |
| t2v-pro | 仅提示词 | Pro 模型，质量更高 |
| i2v-first-frame | 1 张图片 | --image-url 或 --image-file |
| i2v-first-tail | 2 张图片 | --image-url 或 --image-file (两次) |
| i2v-recamera | 1 张图片 + 运镜模板 | --template, --camera-strength |
| i2v-first-pro | 1 张图片 | Pro 模型，质量更高 |

### 6.3 视频提交 (submit)

参数说明：
- `--preset`：视频预设（必填）
- `--prompt`：提示词（必填）
- `--frames`：帧数，默认 `121` (针对 t2v)
- `--aspect-ratio`：宽高比，默认 `16:9` (针对 t2v)
- `--image-url` / `--image-file`：参考图输入（i2v-first-tail 需要 2 个）
- `--template`：运镜模板 ID (针对 i2v-recamera)
- `--camera-strength`：运镜强度 `weak|medium|strong` (针对 i2v-recamera)
- `--wait`：提交后自动等待任务完成
- `--wait-timeout`：等待超时时间，默认 `10m`
- `--download-dir`：完成后自动下载到指定目录
- `--overwrite`：下载时覆盖已存在的文件

示例：

```bash
# 文生视频 (指定协议与 Host)
VOLC_SCHEME=https VOLC_HOST=visual.volcengineapi.com jimeng video submit \
  --preset t2v-720 \
  --prompt "一只在森林中奔跑的小狗" \
  --aspect-ratio 16:9 \
  --format json

# 图生视频 (URL)
jimeng video submit \
  --preset i2v-first \
  --prompt "让图片中的人物微笑" \
  --image-url "https://example.com/input.png" \
  --format json

# 图生视频 (本地文件)
jimeng video submit \
  --preset i2v-first \
  --prompt "让图片中的人物微笑" \
  --image-file "./input.png" \
  --format json

# 首尾帧 (两张图片)
jimeng video submit \
  --preset i2v-first-tail \
  --prompt "从第一张图平滑过渡到第二张图" \
  --image-url "https://example.com/first.png" \
  --image-url "https://example.com/tail.png" \
  --format json

# 720p 版本示例
jimeng video submit \
  --preset i2v-first-720 \
  --prompt "让图片中的人物微笑" \
  --image-file "./input.png" \
  --format json

# 运镜模式示例
jimeng video submit \
  --preset i2v-recamera \
  --prompt "镜头向左平移" \
  --image-url "https://example.com/input.png" \
  --template "horizontal_pan_left" \
  --camera-strength "strong" \
  --format json
```

### 6.4 视频生成命令速查表

#### 文生视频 (Text-to-Video)

```bash
# 720p
jimeng video submit --preset t2v-720 --prompt "一只可爱的猫咪在草地上奔跑" --wait --download-dir outputs/

# 1080p
jimeng video submit --preset t2v-1080 --prompt "一只可爱的猫咪在草地上奔跑" --wait --download-dir outputs/

# Pro
jimeng video submit --preset t2v-pro --prompt "一只可爱的猫咪在草地上奔跑" --wait --download-dir outputs/
```

#### 图生视频 - 首帧 (Image-to-Video First Frame)

**使用本地文件 (1 张图片):**
```bash
# 720p
jimeng video submit --preset i2v-first-720 --prompt "猫咪向前奔跑" --image-file outputs/first.png --wait --download-dir outputs/

# 1080p
jimeng video submit --preset i2v-first --prompt "猫咪向前奔跑" --image-file outputs/first.png --wait --download-dir outputs/

# Pro
jimeng video submit --preset i2v-first-pro --prompt "猫咪向前奔跑" --image-file outputs/first.png --wait --download-dir outputs/
```

**使用 URL (1 张图片):**
```bash
jimeng video submit --preset i2v-first-720 --prompt "猫咪向前奔跑" --image-url "https://example.com/first.png" --wait --download-dir outputs/
```

#### 图生视频 - 首尾帧 (Image-to-Video First + Tail Frame)

**使用本地文件 (2 张图片):**
```bash
# 720p
jimeng video submit --preset i2v-first-tail-720 --prompt "从首帧过渡到尾帧" \
  --image-file outputs/first.png --image-file outputs/tail.png \
  --wait --download-dir outputs/

# 1080p
jimeng video submit --preset i2v-first-tail --prompt "从首帧过渡到尾帧" \
  --image-file outputs/first.png --image-file outputs/tail.png \
  --wait --download-dir outputs/
```

**使用 URL (2 张图片，重复参数):**
```bash
jimeng video submit --preset i2v-first-tail --prompt "从首帧过渡到尾帧" \
  --image-url "https://example.com/first.png" \
  --image-url "https://example.com/tail.png" \
  --wait --download-dir outputs/
```

**使用 URL (2 张图片，逗号分隔):**
```bash
jimeng video submit --preset i2v-first-tail --prompt "从首帧过渡到尾帧" \
  --image-url "https://example.com/first.png,https://example.com/tail.png" \
  --wait --download-dir outputs/
```

#### 图生视频 - 运镜 (Image-to-Video Recamera)

```bash
jimeng video submit --preset i2v-recamera --prompt "镜头向前推进" \
  --image-file outputs/first.png \
  --template horizontal_pan_left \
  --camera-strength strong \
  --wait --download-dir outputs/
```

#### 图片数量要求

| 预设类型 | 图片数量 | 参数示例 |
|:---|:---:|:---|
| `t2v-*` | 0 | 不需要 `--image-url` 或 `--image-file` |
| `i2v-first-*` | 1 | `--image-file first.png` |
| `i2v-first-tail-*` | 2 | `--image-file first.png --image-file tail.png` |
| `i2v-recamera` | 1 | `--image-file first.png` |

> **注意**: `--image-url` 和 `--image-file` 互斥，不能同时使用。

### 6.5 一步式提交 + 等待 + 下载
### 6.4 一步式提交 + 等待 + 下载

```bash
# 文生视频 Pro + 等待 + 下载
jimeng video submit \
  --preset t2v-pro \
  --prompt "一只在森林中奔跑的小狗" \
  --wait \
  --wait-timeout 10m \
  --download-dir ./outputs \
  --overwrite \
  --format json
```

### 6.5 视频查询、等待与下载

```bash
# 查询状态
jimeng video query --task-id <task_id> --preset t2v-720

# 等待任务完成
jimeng video wait --task-id <task_id> --preset t2v-720 --wait-timeout 10m

# 下载结果
jimeng video download --task-id <task_id> --preset t2v-720 --dir ./outputs
```

下载逻辑说明：

- 任务完成时返回 `video_url`
- `download` 命令会自动下载该 URL 并保存为视频文件（如 `.mp4`）

## 7. 输出文件命名规则

为避免覆盖，下载文件名统一增加任务 ID 前缀：

### 7.1 图片下载 (Image)
- **URL 场景**：`<task_id>-<原始文件名>`
- **Base64 场景**：`<task_id>-image-<序号>.png`

### 7.2 视频下载 (Video)
- **命名规则**：`<task_id>.<扩展名>`
- **扩展名获取**：优先从 `video_url` 中提取扩展名，若无法提取则默认为 `.mp4`。

即使连续多次生成到同一个目录，也不会重名覆盖。

## 8. 常见问题与错误处理

### 8.1 迁移指南 (Migration)

从旧版 API 迁移到即梦 4.0 Relay 服务，**无需修改业务代码**。只需更新环境变量：
- `VOLC_HOST`: 指向 Relay Server 地址（如 `relay.example.com`）
- `VOLC_ACCESSKEY` / `VOLC_SECRETKEY`: 使用 Relay Server 生成的凭证
- `VOLC_SCHEME`: 根据 Relay Server 配置选择 `http` 或 `https`

### 8.2 并发限流 `50430` (Concurrent Limit)

当返回类似错误：
`Request Has Reached API Concurrent Limit, Please Try Later`

处理建议：
- **降低并发**：减少同时提交的任务数，或降低图片生成的 `--count`。
- **指数退避**：在代码中实现重试逻辑，建议间隔 2s, 4s, 8s 后重试。
- **服务端队列**：如果使用了 Relay Server，请检查 `UPSTREAM_MAX_QUEUE` 配置，增加排队深度。

### 8.3 任务 done 但无 URL (Base64 Fallback)

这是服务端返回形态差异（主要针对图片生成），客户端已支持图片 base64 回退下载，会自动保存为本地文件，无需手动处理。

### 8.4 视频生成超时

视频生成通常需要 1-5 分钟。如果 `wait` 命令超时，请增加 `--wait-timeout` 参数（默认 10m）。

## 9. 开发验证命令

```bash
go test ./...
go test -race ./...
go vet ./...
go build -o ./bin/jimeng .
```

## 10. 命令速查表 (Cheat Sheet)

| 命令 | 说明 |
|:---|:---|
| `jimeng submit` | 提交图片生成任务 |
| `jimeng query` | 查询图片任务状态 |
| `jimeng wait` | 等待图片任务完成 |
| `jimeng download` | 下载图片生成结果 |
| `jimeng video submit` | 提交视频生成任务 |
| `jimeng video query` | 查询视频任务状态 |
| `jimeng video wait` | 等待视频任务完成 |
| `jimeng video download` | 下载视频生成结果 |

## 11. 参考文档

- 火山引擎即梦 AI 图片生成 4.0：
  `https://www.volcengine.com/docs/85621/1817045`
