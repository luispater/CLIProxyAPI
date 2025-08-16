# CLI 代理 API

[![Mentioned in Awesome Gemini CLI](https://awesome.re/mentioned-badge.svg)](https://github.com/Piebald-AI/awesome-gemini-cli)

[English](README.md) | 中文

一个为 CLI 提供 OpenAI/Gemini/Claude 兼容 API 接口的代理服务器。

现已支持通过 OAuth 登录接入 OpenAI Codex（GPT 系列）。

可与本地或多账户方式配合，使用任何 OpenAI 兼容的客户端与 SDK。

## 功能特性

- 为 CLI 模型提供 OpenAI/Gemini/Claude 兼容的 API 端点
- 新增 OpenAI Codex（GPT 系列）支持（OAuth 登录）
- 支持流式与非流式响应
- 函数调用/工具支持
- 多模态输入（文本、图片）
- 多账户支持与轮询负载均衡（Gemini 与 OpenAI）
- 简单的 CLI 身份验证流程（Gemini 与 OpenAI）
- 支持 Gemini AIStudio API 密钥
- 支持 Gemini CLI 多账户轮询

## 安装

### 前置要求

- Go 1.24 或更高版本
- 有权访问 Gemini CLI 模型的 Google 账户（可选）
- 有权访问 OpenAI Codex/GPT 的 OpenAI 账户（可选）

### 从源码构建

1. 克隆仓库：
   ```bash
   git clone https://github.com/luispater/CLIProxyAPI.git
   cd CLIProxyAPI
   ```

2. 构建应用程序：
   ```bash
   go build -o cli-proxy-api ./cmd/server
   ```

## 使用方法

### 身份验证

您可以分别为 Gemini 和 OpenAI 进行身份验证，二者可同时存在于同一个 `auth-dir` 中并参与负载均衡。

- Gemini（Google）：
  ```bash
  ./cli-proxy-api --login
  ```
  如果您是旧版 gemini code 用户，可能需要指定项目 ID：
  ```bash
  ./cli-proxy-api --login --project_id <your_project_id>
  ```
  本地 OAuth 回调端口为 `8085`。

- OpenAI（Codex/GPT，OAuth）：
  ```bash
  ./cli-proxy-api --codex-login
  ```
  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。本地 OAuth 回调端口为 `1455`。

### 启动服务器

身份验证完成后，启动服务器：

```bash
./cli-proxy-api
```

默认情况下，服务器在端口 8317 上运行。

### API 端点

#### 列出模型

```
GET http://localhost:8317/v1/models
```

#### 聊天补全

```
POST http://localhost:8317/v1/chat/completions
```

请求体示例：

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "你好，你好吗？"
    }
  ],
  "stream": true
}
```

说明：
- 使用 `gemini-*` 模型（如 `gemini-2.5-pro`）走 Gemini，使用 `gpt-*` 模型（如 `gpt-5`）走 OpenAI，服务会自动路由到对应提供商。

#### Claude 消息（SSE 兼容）

```
POST http://localhost:8317/v1/messages
```

### 与 OpenAI 库一起使用

您可以通过将基础 URL 设置为本地服务器来将此代理与任何 OpenAI 兼容的库一起使用：

#### Python（使用 OpenAI 库）

```python
from openai import OpenAI

client = OpenAI(
    api_key="dummy",  # 不使用但必需
    base_url="http://localhost:8317/v1"
)

# Gemini 示例
gemini = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "你好，你好吗？"}]
)

# Codex/GPT 示例
gpt = client.chat.completions.create(
    model="gpt-5",
    messages=[{"role": "user", "content": "用一句话总结这个项目"}]
)

print(gemini.choices[0].message.content)
print(gpt.choices[0].message.content)
```

#### JavaScript/TypeScript

```javascript
import OpenAI from 'openai';

const openai = new OpenAI({
  apiKey: 'dummy', // 不使用但必需
  baseURL: 'http://localhost:8317/v1',
});

// Gemini
const gemini = await openai.chat.completions.create({
  model: 'gemini-2.5-pro',
  messages: [{ role: 'user', content: '你好，你好吗？' }],
});

// Codex/GPT
const gpt = await openai.chat.completions.create({
  model: 'gpt-5',
  messages: [{ role: 'user', content: '用一句话总结这个项目' }],
});

console.log(gemini.choices[0].message.content);
console.log(gpt.choices[0].message.content);
```

## 支持的模型

- gemini-2.5-pro
- gemini-2.5-flash
- gpt-5
- Gemini 模型在需要时自动切换到对应的 preview 版本

## 配置

服务器默认使用位于项目根目录的 YAML 配置文件（`config.yaml`）。您可以使用 `--config` 标志指定不同的配置文件路径：

```bash
./cli-proxy-api --config /path/to/your/config.yaml
```

### 配置选项

| 参数                                    | 类型       | 默认值                | 描述                                                                     |
|---------------------------------------|----------|--------------------|------------------------------------------------------------------------|
| `port`                                | integer  | 8317               | 服务器监听的端口号                                                              |
| `auth-dir`                            | string   | "~/.cli-proxy-api" | 存储身份验证令牌的目录。支持使用 `~` 表示主目录                                             |
| `proxy-url`                           | string   | ""                 | 代理 URL，支持 socks5/http/https 协议，示例：socks5://user:pass@192.168.1.1:1080/ |
| `quota-exceeded`                      | object   | {}                 | 处理配额超限的配置                                                              |
| `quota-exceeded.switch-project`       | boolean  | true               | 当配额超限时是否自动切换到另一个项目                                                     |
| `quota-exceeded.switch-preview-model` | boolean  | true               | 当配额超限时是否自动切换到预览模型                                                      |
| `debug`                               | boolean  | false              | 启用调试模式以进行详细日志记录                                                        |
| `api-keys`                            | string[] | []                 | 可用于验证请求的 API 密钥列表                                                      |
| `generative-language-api-key`         | string[] | []                 | 生成式语言 API 密钥列表                                                         |

### 配置文件示例

```yaml
# 服务器端口
port: 8317

# 身份验证目录（支持 ~ 表示主目录）
auth-dir: "~/.cli-proxy-api"

# 启用调试日志
debug: false

# 代理 URL，支持 socks5/http/https 协议，示例：socks5://user:pass@192.168.1.1:1080/
proxy-url: ""

# 配额超限行为
quota-exceeded:
   switch-project: true # 当配额超限时是否自动切换到另一个项目
   switch-preview-model: true # 当配额超限时是否自动切换到预览模型

# 用于本地身份验证的 API 密钥
api-keys:
  - "your-api-key-1"
  - "your-api-key-2"

# AIStduio Gemini API 的 API 密钥
generative-language-api-key:
  - "AIzaSy...01"
  - "AIzaSy...02"
  - "AIzaSy...03"
  - "AIzaSy...04"
```

### 身份验证目录

`auth-dir` 参数指定身份验证令牌的存储位置。当您运行登录命令时，应用程序将在此目录中创建包含 Google 账户身份验证令牌的 JSON 文件。多个账户可用于轮询。

### API 密钥

`api-keys` 参数允许您定义可用于验证对代理服务器请求的 API 密钥列表。在向 API 发出请求时，您可以在 `Authorization` 标头中包含其中一个密钥：

```
Authorization: Bearer your-api-key-1
```

### 官方生成式语言 API

`generative-language-api-key` 参数允许您定义可用于验证对官方 AIStudio Gemini API 请求的 API 密钥列表。

## 热更新

服务会监听配置文件与 `auth-dir` 目录的变化并自动重新加载客户端与配置。您可以在运行中新增/移除 Gemini/OpenAI 的令牌 JSON 文件，无需重启服务。

## Gemini CLI 多账户负载均衡

启动 CLI 代理 API 服务器，然后将 `CODE_ASSIST_ENDPOINT` 环境变量设置为 CLI 代理 API 服务器的 URL。

```bash
export CODE_ASSIST_ENDPOINT="http://127.0.0.1:8317"
```

服务器将中继 `loadCodeAssist`、`onboardUser` 和 `countTokens` 请求。并自动在多个账户之间轮询文本生成请求。

> [!NOTE]  
> 此功能仅允许本地访问，因为找不到一个可以验证请求的方法。   
> 所以只能强制只有 `127.0.0.1` 可以访问。

## Claude Code 的使用方法

启动 CLI Proxy API 服务器, 设置如下系统环境变量 `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL`

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gemini-2.5-pro
export ANTHROPIC_SMALL_FAST_MODEL=gemini-2.5-flash
```

或者

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gpt-5
export ANTHROPIC_SMALL_FAST_MODEL=codex-mini-latest
```


## 使用 Docker 运行

运行以下命令进行登录（Gemini OAuth，端口 8085）：

```bash
docker run --rm -p 8085:8085 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --login
```

运行以下命令进行登录（OpenAI OAuth，端口 1455）：

```bash
docker run --rm -p 1455:1455 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --codex-login
```

运行以下命令启动服务器：

```bash
docker run --rm -p 8317:8317 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest
```

## 贡献

欢迎贡献！请随时提交 Pull Request。

1. Fork 仓库
2. 创建您的功能分支（`git checkout -b feature/amazing-feature`）
3. 提交您的更改（`git commit -m 'Add some amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 打开 Pull Request

## 许可证

此项目根据 MIT 许可证授权 - 有关详细信息，请参阅 [LICENSE](LICENSE) 文件。
