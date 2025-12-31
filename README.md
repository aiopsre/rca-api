# Project rca-api

rca-api 是一个基于 Go 语言开发的现代化微服务应用，采用简洁架构设计，具有代码质量高、扩展能力强、符合 Go 编码及最佳实践等特点。

rca-api 具有以下特性：
- 软件架构：采用简洁架构设计，确保项目结构清晰、易维护；
- 高频 Go 包：使用了 Go 项目开发中常用的包，如 gin、otel、gorm、gin、uuid、cobra、viper、pflag、resty、govalidator、slog、protobuf、casbin、onexstack 等；
- 目录结构：遵循 [project-layout](https://github.com/golang-standards/project-layout) 规范，采用标准化的目录结构；
- 认证与授权：实现了基于 JWT 的认证和基于 Casbin 的授权功能；
- 错误处理：设计了独立的错误包及错误码管理机制；
- 构建与管理：使用高质量的 Makefile 对项目进行管理；
- 代码质量：通过 golangci-lint 工具对代码进行静态检查，确保代码质量；
- 测试覆盖：包含单元测试、性能测试、模糊测试和示例测试等多种测试案例；
- 丰富的 Web 功能：支持 Trace ID、优雅关停、中间件、跨域处理、异常恢复等功能；
- 多种数据交换格式：支持 JSON 和 Protobuf 数据格式的交换；
- 开发规范：遵循多种开发规范，包括代码规范、版本规范、接口规范、日志规范、错误规范以及提交规范等；
- API 设计：接口设计遵循 RESTful API 规范；
- 项目具有 Dockerfile，并且 Dockerfile 符合最佳实践；

## Getting Started

### Prerequisites

在开始之前，请确保您的开发环境中安装了以下工具：

**必需工具：**
- [Go](https://golang.org/dl/) 1.25.3 或更高版本
- [Git](https://git-scm.com/) 版本控制工具
- [Make](https://www.gnu.org/software/make/) 构建工具

**可选工具：**
- [Docker](https://www.docker.com/) 容器化部署
- [golangci-lint](https://golangci-lint.run/) 代码静态检查

**验证安装：**

```bash
$ go version  
go version go1.25.3 linux/amd64  
$ make --version  
GNU Make 4.3  
```

### Building

> 提示：项目配置文件配置项 `metadata.makefileMode` 不能为 `none`，如果为 `none` 需要自行构建。

在项目根目录下，执行以下命令构建项目：

**1. 安装依赖工具和包**

```bash
$ make deps  # 安装项目所需的开发工具  
$ go mod tidy # 下载 Go 模块依赖  
```

**2. 生成代码**

```bash
$ make protoc # generate gRPC code  
$ go get cloud.google.com/go/compute@latest cloud.google.com/go/compute/metadata@latest  
$ go mod tidy # tidy dependencies  
$ go generate ./... # run all go:generate directives  
```

**3. 构建应用**

```bash
$ make build # build all binary files locate in cmd/  
```

**构建结果：**

```bash
_output/platforms/  
├── linux/  
│   └── amd64/  
│       └── rca-apiserver  # apiserver 服务二进制文件  
└── darwin/  
    └── amd64/  
        └── rca-apiserver  
```

### Running

启动服务有多种方式：

**1. 使用构建的二进制文件运行**

```bash  
# 启动 apiserver 服务  
$ _output/platforms/linux/amd64/rca-apiserver --config configs/rca-apiserver.yaml  
# 服务将在以下端口启动：  
# - HTTP API: http://localhost:5555
# - Health Check: http://localhost:5555/healthz  
# - Metrics: http://localhost:5555/metrics  
$ curl http://localhost:5555/healthz # 测试：打开另外一个终端，调用健康检查接口  
```

**2. 使用 Docker 运行**

```bash
# 构建镜像  
$ make image
$ docker run --name rca-apiserver -v configs/rca-apiserver.yaml:/etc/rca-apiserver.yaml -p 5555:5555 hub.17ker.top/rca/rca-apiserver:latest -c /etc/rca-apiserver.yaml
```

**配置文件示例：**  

rca-apiserver 配置文件 `configs/rca-apiserver.yaml`：

```yaml

addr: 0.0.0.0:5555 # 服务监听地址
timeout: 30s # 服务端超时
otel:
  endpoint: 127.0.0.1:4327
  service-name: rca-apiserver
  # 支持 otel、console、file、slog、classic、hybrid：
  # - otel:
  #   - logs -> otel collector agent
  #   - metrics -> otel collector agent
  #   - traces -> otel collector agent
  # - file:
  #   - logs -> 以 opentelemetry 格式，输出到 <output-dir>/logs.json 文件中
  #   - metrics -> 以 opentelemetry 格式，输出到 <output-dir>/metrics.json 文件中
  #   - traces -> 以 opentelemetry 格式，输出到 <output-dir>/traces.json 文件中
  # - console:
  #   - logs -> 以 opentelemetry 格式，输出到标准输出中
  #   - metrics -> 以 opentelemetry 格式，输出到标准输出中
  #   - traces -> 以 opentelemetry 格式，输出到标准输出中
  # - classic:
  #   - logs -> 输出到标准输出中（自定义结构化日志）
  #   - metrics -> 输出到 promethus export 中
  #   - traces -> 关闭 trace
  # - hybrid:
  #   - logs -> 输出到标准输出中（自定义结构化日志）
  #   - metrics -> 输出到 promethus export 中
  #   - traces -> 输出到 otel collector agent 中
  # output-dir: ./otel # file mode 下，文件输出路径
  output-mode: classic
  level: debug
  add-source: true
  slog: # 该配置项只有 output-mode 为 hybrid, classic 时生效
    format: json
    time-format: "2006-01-02 15:04:05"
    output: stdout # 支持 stdout, stderr, or file path
mysql:
  addr: 127.0.0.1:3306 # MySQL 的访问地址
  username: onex # MySQL 用户名
  password: "onex(#)666" # MySQL 密码
  database: onex # MySQL 数据库名
  max-connection-life-time: 10s # 单个数据库连接的最大存活时间，超过这个时间，连接会被关闭并重建
  max-idle-connections: 100 # 连接池中允许空闲连接的最大数量
  max-open-connections: 100 # 连接池中允许同时打开的最大连接数（包括正在用的和空闲的）
```  

### Linting

建议本仓库使用两级 lint：

```bash
$ make lint-new                   # 增量门槛：仅检查变更包
$ LINT_PKGS=./internal/... make lint-new  # 非 git 场景 fallback
$ make lint                       # 全量检查：用于后续历史问题还债
```

### CI / Gate（门禁分层）

为避免 PR 被重型 E2E 拖慢，同时又能持续覆盖关键回归，本仓库提供两级门禁脚本：

```bash
# PR Gate：快、确定性、默认无外部依赖
$ ./scripts/ci_pr_gate.sh

# PR Gate（可选）带最小 E2E：需要你先启动 rca-api
$ RUN_E2E=1 SCOPES='*' RUN_QUERY=0 BASE_URL='http://127.0.0.1:5555' ./scripts/ci_pr_gate.sh

# Nightly Gate：重型回归（风暴/运营回归），需要你先启动 rca-api（以及对应 orchestrator）
$ SCOPES='*' BASE_URL='http://127.0.0.1:5555' ./scripts/ci_nightly_gate.sh
```

## Versioning

本项目遵循 [语义版本控制](https://semver.org/lang/zh-CN/) 规范。

## Authors

### 主要贡献者

- **zhanjie.me** - *项目创建者和维护者* - [i@zhanjie.me](mailto:i@zhanjie.me)
  - 项目架构设计
  - 核心功能开发
  - 技术方案制定

### 贡献者列表

感谢所有为本项目做出贡献的开发者们！

<!-- 这里会自动显示贡献者头像 -->
<a href="github.com/aiopsre/rca-api/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=github.com/aiopsre/rca-api" />
</a>

*贡献者列表由 [contrib.rocks](https://contrib.rocks) 生成*

## 附录

### 项目结构

```bash
rca-api/  
├── cmd/                     # 应用程序入口  
│   └── rca-apiserver/       # apiserver 服务  
│       └── main.go          # 主函数  
├── internal/                # 私有应用程序代码  
│   └── apiserver/             # apiserver 内部包  
│       ├── biz/             # 业务逻辑层  
│       ├── handler/         # gin 处理器  
│       ├── model/           # GORM 数据模型  
│       ├── pkg/             # 内部工具包  
│       └── store/           # 数据访问层  
├── pkg/                     # 公共库代码  
│   ├── api/                 # API 定义  
├── examples/                # 示例代码  
│   └── client/              # 客户端示例  
├── configs/                 # 配置文件  
├── docs/                    # 项目文档  
├── build/                   # 构建配置  
│   └── docker/              # Docker 文件  
├── scripts/                 # 构建和部署脚本  
├── third_party/             # 第三方依赖  
├── Makefile                 # 构建配置  
├── go.mod                   # Go 模块文件  
├── go.sum                   # Go 模块校验文件  
└── README.md                # 项目说明文档  
```

### 相关链接

- [项目文档](docs/)
- [问题追踪](github.com/aiopsre/rca-api/issues)
- [讨论区](github.com/aiopsre/rca-api/discussions)
- [项目看板](github.com/aiopsre/rca-api/projects)
- [发布页面](github.com/aiopsre/rca-api/releases)

