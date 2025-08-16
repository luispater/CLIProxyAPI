# CLI Proxy API

[![Mentioned in Awesome Gemini CLI](https://awesome.re/mentioned-badge.svg)](https://github.com/Piebald-AI/awesome-gemini-cli)

English | [中文](README_CN.md)

A proxy server that provides OpenAI/Gemini/Claude compatible API interfaces for CLI. 

It now also supports OpenAI Codex (GPT models) via OAuth.

so you can use local or multi‑account CLI access with OpenAI‑compatible clients and SDKs.

## Features

- OpenAI/Gemini/Claude compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Streaming and non-streaming responses
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round‑robin load balancing (Gemini and OpenAI)
- Simple CLI authentication flows (Gemini and OpenAI)
- Generative Language API Key support
- Gemini CLI multi‑account load balancing

## Installation

### Prerequisites

- Go 1.24 or higher
- A Google account with access to Gemini CLI models (optional)
- An OpenAI account for Codex/GPT access (optional)

### Building from Source

1. Clone the repository:
   ```bash
   git clone https://github.com/luispater/CLIProxyAPI.git
   cd CLIProxyAPI
   ```

2. Build the application:
   ```bash
   go build -o cli-proxy-api ./cmd/server
   ```

## Usage

### Authentication

You can authenticate for Gemini and/or OpenAI. Both can coexist in the same `auth-dir` and will be load balanced.

- Gemini (Google):
  ```bash
  ./cli-proxy-api --login
  ```
  If you are an old gemini code user, you may need to specify a project ID:
  ```bash
  ./cli-proxy-api --login --project_id <your_project_id>
  ```
  The local OAuth callback uses port `8085`.

- OpenAI (Codex/GPT via OAuth):
  ```bash
  ./cli-proxy-api --codex-login
  ```
  Options: add `--no-browser` to print the login URL instead of opening a browser. The local OAuth callback uses port `1455`.

### Starting the Server

Once authenticated, start the server:

```bash
./cli-proxy-api
```

By default, the server runs on port 8317.

### API Endpoints

#### List Models

```
GET http://localhost:8317/v1/models
```

#### Chat Completions

```
POST http://localhost:8317/v1/chat/completions
```

Request body example:

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "Hello, how are you?"
    }
  ],
  "stream": true
}
```

Notes:
- Use a `gemini-*` model for Gemini (e.g., `gemini-2.5-pro`) or a `gpt-*` model for OpenAI (e.g., `gpt-5`). The proxy will route to the correct provider automatically.

#### Claude Messages (SSE-compatible)

```
POST http://localhost:8317/v1/messages
```

### Using with OpenAI Libraries

You can use this proxy with any OpenAI-compatible library by setting the base URL to your local server:

#### Python (with OpenAI library)

```python
from openai import OpenAI

client = OpenAI(
    api_key="dummy",  # Not used but required
    base_url="http://localhost:8317/v1"
)

# Gemini example
gemini = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello, how are you?"}]
)

# Codex/GPT example
gpt = client.chat.completions.create(
    model="gpt-5",
    messages=[{"role": "user", "content": "Summarize this project in one sentence."}]
)
print(gemini.choices[0].message.content)
print(gpt.choices[0].message.content)
```

#### JavaScript/TypeScript

```javascript
import OpenAI from 'openai';

const openai = new OpenAI({
  apiKey: 'dummy', // Not used but required
  baseURL: 'http://localhost:8317/v1',
});

// Gemini
const gemini = await openai.chat.completions.create({
  model: 'gemini-2.5-pro',
  messages: [{ role: 'user', content: 'Hello, how are you?' }],
});

// Codex/GPT
const gpt = await openai.chat.completions.create({
  model: 'gpt-5',
  messages: [{ role: 'user', content: 'Summarize this project in one sentence.' }],
});

console.log(gemini.choices[0].message.content);
console.log(gpt.choices[0].message.content);
```

## Supported Models

- gemini-2.5-pro
- gemini-2.5-flash
- gpt-5
- Gemini models auto‑switch to preview variants when needed

## Configuration

The server uses a YAML configuration file (`config.yaml`) located in the project root directory by default. You can specify a different configuration file path using the `--config` flag:

```bash
./cli-proxy-api --config /path/to/your/config.yaml
```

### Configuration Options

| Parameter                             | Type     | Default            | Description                                                                                  |
|---------------------------------------|----------|--------------------|----------------------------------------------------------------------------------------------|
| `port`                                | integer  | 8317               | The port number on which the server will listen                                              |
| `auth-dir`                            | string   | "~/.cli-proxy-api" | Directory where authentication tokens are stored. Supports using `~` for home directory      |
| `proxy-url`                           | string   | ""                 | Proxy url, support socks5/http/https protocol, example: socks5://user:pass@192.168.1.1:1080/ |
| `quota-exceeded`                      | object   | {}                 | Configuration for handling quota exceeded                                                    |
| `quota-exceeded.switch-project`       | boolean  | true               | Whether to automatically switch to another project when a quota is exceeded                  |
| `quota-exceeded.switch-preview-model` | boolean  | true               | Whether to automatically switch to a preview model when a quota is exceeded                  |
| `debug`                               | boolean  | false              | Enable debug mode for verbose logging                                                        |
| `api-keys`                            | string[] | []                 | List of API keys that can be used to authenticate requests                                   |
| `generative-language-api-key`         | string[] | []                 | List of Generative Language API keys                                                         |

### Example Configuration File

```yaml
# Server port
port: 8317

# Authentication directory (supports ~ for home directory)
auth-dir: "~/.cli-proxy-api"

# Enable debug logging
debug: false

# Proxy url, support socks5/http/https protocol, example: socks5://user:pass@192.168.1.1:1080/
proxy-url: ""

# Quota exceeded behavior
quota-exceeded:
   switch-project: true # Whether to automatically switch to another project when a quota is exceeded
   switch-preview-model: true # Whether to automatically switch to a preview model when a quota is exceeded

# API keys for authentication
api-keys:
  - "your-api-key-1"
  - "your-api-key-2"

# API keys for official Generative Language API
generative-language-api-key:
  - "AIzaSy...01"
  - "AIzaSy...02"
  - "AIzaSy...03"
  - "AIzaSy...04"
```

### Authentication Directory

The `auth-dir` parameter specifies where authentication tokens are stored. When you run the login command, the application will create JSON files in this directory containing the authentication tokens for your Google accounts. Multiple accounts can be used for load balancing.

### API Keys

The `api-keys` parameter allows you to define a list of API keys that can be used to authenticate requests to your proxy server. When making requests to the API, you can include one of these keys in the `Authorization` header:

```
Authorization: Bearer your-api-key-1
```

### Official Generative Language API

The `generative-language-api-key` parameter allows you to define a list of API keys that can be used to authenticate requests to the official Generative Language API.

## Hot Reloading

The server watches the config file and the `auth-dir` for changes and reloads clients and settings automatically. You can add or remove Gemini/OpenAI token JSON files while the server is running; no restart is required.

## Gemini CLI with multiple account load balancing

Start CLI Proxy API server, and then set the `CODE_ASSIST_ENDPOINT` environment variable to the URL of the CLI Proxy API server.

```bash
export CODE_ASSIST_ENDPOINT="http://127.0.0.1:8317"
```

The server will relay the `loadCodeAssist`, `onboardUser`, and `countTokens` requests. And automatically load balance the text generation requests between the multiple accounts.

> [!NOTE]  
> This feature only allows local access because I couldn't find a way to authenticate the requests.   
> I hardcoded `127.0.0.1` into the load balancing.

## Claude Code with multiple account load balancing

Start CLI Proxy API server, and then set the `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL` environment variables.

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gemini-2.5-pro
export ANTHROPIC_SMALL_FAST_MODEL=gemini-2.5-flash
```

or

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gpt-5
export ANTHROPIC_SMALL_FAST_MODEL=codex-mini-latest
```

## Run with Docker

Run the following command to login (Gemini OAuth on port 8085): 

```bash
docker run --rm -p 8085:8085 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --login
```

Run the following command to login (OpenAI OAuth on port 1455):

```bash
docker run --rm -p 1455:1455 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --codex-login
```

Run the following command to start the server:

```bash
docker run --rm -p 8317:8317 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
