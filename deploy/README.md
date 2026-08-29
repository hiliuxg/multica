# Multica v0.4.35 线上部署与执行电脑接入

本文对应以下交付目标：

| 项目 | 值 |
| --- | --- |
| Multica 版本 | `v0.4.35` |
| Git 提交 | `09a2410e882be8435bd6c4a26e03f7e288038203` |
| 服务端平台 | `linux/amd64` |
| backend/Web 来源 | 当前本地 checkout 源码 |
| 执行电脑 CLI | `darwin/arm64` |
| 服务端目录 | `deploy/dist/multica-v0.4.35-linux-amd64-server/` |
| CLI 目录 | `dist/multica-cli-v0.4.35-darwin-arm64/` |

当前 checkout 与 GitHub 官方最新稳定 Release `v0.4.35` 完全一致。服务端离线包中的 `multica-backend:v0.4.35-local` 和 `multica-web:v0.4.35-local` 都由当前本地 checkout 的源码构建；`nginx:1.29.8-alpine` 和 `pgvector/pgvector:pg17` 是从外部镜像仓库拉取并固定选择的 `linux/amd64` 镜像。开发机器 CLI 使用同一 Release 的 `darwin/arm64` 官方产物。

## 一、整体架构是什么？

```text
浏览器 / 办公网 CLI ──HTTPS/WSS──> 外部 Nginx ──HTTP/WS──> Compose Nginx :18080
                                                               ├──> Web :3000
                                                               └──> API :8080 ──> PostgreSQL

办公网开发机器：multica 守护进程 ──> Codex CLI ──> 本地代码/Git
```

- 线上服务器运行 Web、API 和 PostgreSQL，负责管理工作区、任务、智能体、执行记录和权限。
- 开发机器运行一个 `multica` CLI。守护进程就是这个 CLI 的 `multica daemon ...` 子命令，不存在第二个 daemon CLI 安装包。
- 守护进程在开发机器上启动 Codex CLI。命令执行、代码修改和测试都发生在开发机器。
- 线上服务器不会主动扫描局域网，也不会反向连接开发机器。守护进程主动连接线上 API，因此开发机器不需要公网 IP 或端口映射。

## 二、交付包里有什么？

服务端包：

```text
multica-v0.4.35-linux-amd64-server/
├── .env.example
├── README.md
├── VERSION
├── docker-compose.yml
├── load-images.sh
├── nginx.conf
├── prepare-env.sh
├── SHA256SUMS
└── images/
    ├── multica-backend-v0.4.35-linux-amd64.tar
    ├── multica-web-v0.4.35-linux-amd64.tar
    ├── nginx-1.29.8-alpine-linux-amd64.tar
    └── pgvector-pg17-linux-amd64.tar
```

开发机器 CLI 包：

```text
multica-cli-0.4.35-darwin-arm64.tar.gz
```

CLI 压缩包内已经包含 `multica` 可执行文件、LICENSE、NOTICE 和中英文 README。

## 三、本次离线镜像是怎样打包的？

推荐直接在项目根目录运行完整打包脚本：

```bash
chmod +x deploy/build-linux-amd64.sh
./deploy/build-linux-amd64.sh
```

如果同版本产物已经存在并确认需要重新生成：

```bash
./deploy/build-linux-amd64.sh --force
```

脚本默认把基础镜像和 BuildKit 中间层持久缓存在 `deploy/.cache/`。第一次构建需要下载镜像并安装 Go/pnpm 依赖；后续构建会显示 `CACHE HIT` 和 `BUILD CACHE: ... available`，即使脚本删除了临时 Colima，也能复用这些缓存。`--force` 只覆盖交付产物，不会清除缓存。

基础镜像标签可能更新。需要主动重新下载 Go、Alpine、Node、Nginx 和 pgvector 镜像时运行：

```bash
./deploy/build-linux-amd64.sh --force --refresh-images
```

缓存默认不进入 Git，也不会被放进最终服务器压缩包。如果要把缓存放到其他磁盘：

```bash
./deploy/build-linux-amd64.sh --cache-dir /Volumes/build-cache/multica
```

查看缓存大小可以运行 `du -sh deploy/.cache`；删除 `deploy/.cache` 后，下次会恢复为一次完整冷构建。

默认输出：

```text
deploy/dist/
├── multica-v0.4.35-SHA256SUMS
├── multica-v0.4.35-linux-amd64-server.tar.gz
└── multica-v0.4.35-linux-amd64-server/
```

脚本会通过 `deploy/Dockerfile.linux.amd64` 完成 backend 交叉编译和运行时封装，并完成 Web 镜像构建、Nginx/pgvector 缓存或拉取、Nginx 配置检查、平台验证、Compose 配置验证、SHA256 生成和最终压缩。基础镜像 tar 缓存在 `deploy/.cache/images/`，backend/Web 的可移植 BuildKit 缓存在 `deploy/.cache/buildx/`。以下内容是脚本内部构建步骤的展开说明。

本机代理为 `127.0.0.1:7897`。以下命令都在项目根目录执行。先声明版本、输出目录和代理：

```bash
version=v0.4.35
commit=09a2410e882be8435bd6c4a26e03f7e288038203
build_date="$(git show -s --format=%cI "$commit")"
image_dir="$PWD/deploy/dist/multica-v0.4.35-linux-amd64-server/images"

export HTTP_PROXY=http://127.0.0.1:7897
export HTTPS_PROXY=http://127.0.0.1:7897
export http_proxy=$HTTP_PROXY
export https_proxy=$HTTPS_PROXY
mkdir -p "$image_dir"
```

backend 使用当前 checkout 的 `server/` 源码，并由 `deploy/Dockerfile.linux.amd64` 完成完整的多阶段构建。Go builder 运行在 Docker 构建机的原生架构上，再通过 `GOOS=linux GOARCH=amd64` 生成目标二进制，因此不会在 Apple Silicon 上通过 QEMU 运行 amd64 Go 编译器；最终运行时固定为 amd64 Alpine。本机无需预先安装 Go：

```bash
docker buildx build \
  --platform linux/amd64 \
  --build-arg VERSION="$version" \
  --build-arg COMMIT="$commit" \
  --build-arg DATE="$build_date" \
  --tag "multica-backend:${version}-local" \
  --output "type=docker,dest=$image_dir/multica-backend-v0.4.35-linux-amd64.tar" \
  --file deploy/Dockerfile.linux.amd64 \
  .
```

Web 使用当前 checkout 的 `apps/web/` 与 `packages/` 源码，通过仓库根目录的 `Dockerfile.web` 构建。Next.js 生产构建建议给 Docker 虚拟机至少 8 GiB 内存，本次打包使用 12 GiB；4 GiB 环境可能在优化阶段报 `cannot allocate memory`。Colima 容器访问 macOS 宿主机代理时要使用 `host.lima.internal`，不能使用容器自己的 `127.0.0.1`：

```bash
docker buildx build \
  --platform linux/amd64 \
  --build-arg HTTP_PROXY=http://host.lima.internal:7897 \
  --build-arg HTTPS_PROXY=http://host.lima.internal:7897 \
  --build-arg NEXT_PUBLIC_APP_VERSION="$version" \
  --tag "multica-web:${version}-local" \
  --output "type=docker,dest=$image_dir/multica-web-v0.4.35-linux-amd64.tar" \
  --file Dockerfile.web \
  .
```

如果使用 Docker Desktop，把 `host.lima.internal` 改成 `host.docker.internal`。首次构建或使用 `--refresh-images` 时，基础镜像和 pnpm 依赖仍然需要通过代理访问外网；后续构建优先复用持久缓存。最终生成的 Multica backend/Web 镜像内容来自当前本地源码，不是从 GHCR 下载的 Multica 成品镜像。

Nginx 网关和 PostgreSQL/pgvector 成品镜像直接从外部仓库拉取，并固定选择 `linux/amd64` manifest：

```bash
crane pull --platform linux/amd64 \
  nginx:1.29.8-alpine \
  "$image_dir/nginx-1.29.8-alpine-linux-amd64.tar"

crane pull --platform linux/amd64 \
  pgvector/pgvector:pg17 \
  "$image_dir/pgvector-pg17-linux-amd64.tar"
```

最终四个归档的镜像标签分别是 `multica-backend:v0.4.35-local`、`multica-web:v0.4.35-local`、`nginx:1.29.8-alpine` 和 `pgvector/pgvector:pg17`，平台均为 `linux/amd64`。

## 四、怎样把服务端包上传到 Linux 服务器？

在开发机器运行：

```bash
scp deploy/dist/multica-v0.4.35-linux-amd64-server.tar.gz \
  root@YOUR_SERVER:/opt/
```

在服务器运行：

```bash
cd /opt
tar -xzf multica-v0.4.35-linux-amd64-server.tar.gz
cd multica-v0.4.35-linux-amd64-server

./load-images.sh
```

服务器只需要：

- Linux `amd64`；
- Docker Engine；
- Docker Compose v2，也就是 `docker compose`；
- OpenSSL；
- 一个能把 HTTPS/WSS 转发到服务器私网地址的外部 Nginx 或同类入口。

离线启动时不需要服务器访问 Docker Hub 或 GHCR。

## 五、是否必须提供域名？

程序本身不强制域名，但公网部署强烈建议使用域名和 HTTPS。一个域名已经足够，例如：

```text
multica.example.com
```

原因如下：

- 浏览器登录、cookie 和回调地址需要稳定 origin；
- 守护进程需要稳定的 HTTPS/WSS 地址；
- 直接暴露 HTTP IP 会增加登录凭据和执行记录泄漏风险；
- Compose 默认只把 `3000`、`8080` 和网关 `18080` 绑定到服务器的 `127.0.0.1`；需要外部 Nginx 跨机器接入时，只把 `18080` 绑定到服务器的精确私网 IP。

内网测试可以使用 IP 和 HTTP，但仍应通过反向代理访问。不要把 Compose 端口绑定改成 `0.0.0.0`。

## 六、怎样配置域名和环境变量？

先生成环境文件。第二个参数是 Compose Nginx 网关监听的服务器私网 IP；如果外部 Nginx 与 Docker 在同一台服务器上，可省略它并保留 `127.0.0.1`。脚本会拒绝覆盖已有 `.env`，也会拒绝 `0.0.0.0`：

```bash
./prepare-env.sh multica.example.com 10.34.81.19
```

检查 `.env`，至少确认这些值：

```dotenv
FRONTEND_ORIGIN=https://multica.example.com
MULTICA_APP_URL=https://multica.example.com
MULTICA_PUBLIC_URL=https://multica.example.com
MULTICA_DAEMON_SERVER_URL=https://multica.example.com
GOOGLE_REDIRECT_URI=https://multica.example.com/auth/callback
GATEWAY_BIND_ADDRESS=10.34.81.19
GATEWAY_PORT=18080
MULTICA_IMAGE_TAG=v0.4.35-local
```

如果要用邮件发送登录验证码，配置 Resend 或 SMTP。暂时不配置邮件也能启动，验证码会打印在 backend 日志里。

Compose 内置的 `nginx.conf` 已经完成单域名路径分流：API、认证、上传和 WebSocket 进入 backend，其余请求进入 frontend。外部 Nginx 只需把整个域名代理到 `10.34.81.19:18080`，并保留 Host、原始 HTTPS scheme 和 WebSocket Upgrade。外部 Nginx 的 `http` 段加入：

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

域名的 `server` 段保留现有证书配置，并使用一个统一的 location：

```nginx
location / {
    proxy_pass http://10.34.81.19:18080;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
}
```

只允许外部 Nginx 的源 IP 访问服务器 `18080`。PostgreSQL、`3000`、`8080` 不需要离开服务器；不要把任何 Compose 端口绑定到 `0.0.0.0`。

## 七、怎样启动 Docker Compose？

```bash
docker compose --env-file .env -f docker-compose.yml up -d --pull never
```

检查状态：

```bash
docker compose --env-file .env -f docker-compose.yml ps
docker compose --env-file .env -f docker-compose.yml logs --tail 100 backend
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS -H 'Host: multica.example.com' \
  -H 'X-Forwarded-Proto: https' \
  http://10.34.81.19:18080/readyz
curl -fsS https://multica.example.com/health
```

正常的 backend 就绪响应类似：

```json
{"status":"ok","checks":{"db":"ok","migrations":"ok"}}
```

如果还没有配置邮件，从日志读取登录验证码：

```bash
docker compose --env-file .env -f docker-compose.yml logs backend \
  | grep "Verification code"
```

常用运维命令：

```bash
# 查看日志
sudo docker compose --env-file .env -f docker-compose.yml logs -f

# 重启
sudo docker compose --env-file .env -f docker-compose.yml restart

# 停止但保留数据库卷
sudo docker compose --env-file .env -f docker-compose.yml down

# 升级或修改 .env 后重新创建容器
sudo docker compose --env-file .env -f docker-compose.yml up -d --pull never

# 确认目标卷
sudo docker volume inspect multica_pgdata

# 只删除 PostgreSQL 卷
sudo docker volume rm multica_pgdata
```

不要运行 `docker compose down -v`，除非确定要删除 PostgreSQL 和上传文件数据。

## 八、开发机器怎样安装 Multica CLI？

把 CLI 压缩包复制到开发机器后运行：

```bash
tar -xzf multica-cli-0.4.35-darwin-arm64.tar.gz
sudo install -m 0755 multica /usr/local/bin/multica
multica version
```

这台机器还需要安装并登录 Codex CLI：

```bash
codex --version
codex
```

Codex 必须能在当前终端独立完成请求。Multica 不会替你安装 Codex，也不会接收 Codex 的登录 token。

## 九、开发机器怎样连接线上 Multica？

单域名部署中，`--server-url` 和 `--app-url` 使用同一个地址：

```bash
multica setup self-host \
  --server-url https://multica.example.com \
  --app-url https://multica.example.com
```

该命令会：

1. 请求 `https://multica.example.com/health` 检查 API；
2. 打开浏览器完成 Multica 登录；
3. 把 Multica 个人访问令牌和服务器地址保存到本机 profile；
4. 检测 `PATH` 中的 Codex；
5. 在后台启动守护进程。

检查结果：

```bash
multica auth status
multica daemon status
multica daemon logs -f
command -v codex
codex --version
```

然后打开 WebUI 的“设置 → 运行时”。开发机器和 Codex 应显示为在线。

## 十、守护进程是怎样发现线上服务器的？

它不进行自动网络发现，也不使用 mDNS、广播或服务器反连。地址来自第一次运行的：

```bash
multica setup self-host --server-url ... --app-url ...
```

CLI 把地址和登录凭据保存在本机：

```text
~/.multica/config.json
```

命名 profile 则保存在：

```text
~/.multica/profiles/<profile>/config.json
```

守护进程随后主动建立到 `<server-url>/api/daemon/ws` 的 WebSocket 长连接，并保留轮询作为连接中断时的补充。服务器通过这条连接通知新执行；守护进程领取执行后启动本地 Codex，再把状态、日志和结果写回线上 API。

因此网络要求是：

| 位置 | 必需网络 |
| --- | --- |
| 线上服务器 | 对浏览器和守护进程开放 HTTPS `443` |
| 开发机器 | 能出站访问 Multica 域名 `443` |
| 开发机器 | 能访问 OpenAI/Codex 所需网络 |
| 开发机器 | 能访问 Git 仓库、包管理器等开发依赖 |

开发机器不需要开放任何入站端口。

## 十一、WebUI 正常但运行时离线怎么办？

按顺序检查：

```bash
multica auth status
multica daemon status
multica daemon logs --lines 200
command -v codex
codex --version
```

常见原因：

- `/health` 没有被反向代理到 backend；
- `/api/daemon/ws` 没有保留 WebSocket Upgrade；
- 开发机器无法解析域名或不信任 TLS 证书；
- Codex 不在守护进程的 `PATH` 中；
- Multica 令牌过期或被吊销；
- 笔记本休眠、关机或守护进程退出。

修改代理或安装 Codex 后运行：

```bash
multica daemon restart
```

## 十二、运行在个人笔记本上安全吗？

守护进程启动的 Codex 默认拥有运行该守护进程的操作系统用户权限。该用户可以读取或修改的文件，智能体通常也可以访问。

推荐使用以下任一方式收窄权限：

- 单独创建一个本机用户运行守护进程；
- 使用容器或虚拟机；
- 使用权限最小化的 Git/SSH 凭据；
- 不在该用户目录中存放无关的生产凭据。

线上服务器不会自动上传整个本地工作目录，但会保存任务上下文、执行记录、评论和智能体写回的结果，其中可能包含代码片段。

## 十三、怎样备份和升级？

需要备份的主要 Docker 卷是 PostgreSQL 数据和 backend 上传文件。升级前至少执行一次数据库备份：

```bash
docker compose --env-file .env -f docker-compose.yml exec -T postgres \
  pg_dump -U multica -d multica -Fc > multica-$(date +%F).dump
```

升级步骤：

1. 下载新版本的 `linux/amd64` 镜像包；
2. 执行 `docker load`；
3. 修改 `.env` 中的 `MULTICA_IMAGE_TAG`；
4. 运行 `docker compose ... up -d --pull never`；
5. 升级开发机器上的 `multica` CLI，并重启守护进程。

backend 每次启动时会自动执行数据库 migration，不需要单独运行 migration 命令。
