# 家庭物资管理（family-inventory）

一个自托管的家庭物品 / 位置台账工具：Go 静态后端 + 内嵌原生 HTML/JS 前端，单容器部署，数据以 JSON 文件 + 图片文件持久化，适合家庭内网使用。

## 功能特性

- **账号登录 / 首次注册**：首次打开注册管理员账号，之后登录使用；密码 bcrypt 哈希存储，数据接口需登录令牌访问。
- **层级位置树**：位置支持多级父子结构（如 `主卧 → 主卧衣柜 → 第一层`），按父位置筛选自动包含全部后代位置。
- **物品台账**：名称、位置、数量、条码 / 编号、标签、备注、配图；网格 / 列表两种视图；关键字搜索命中名称、备注、位置完整路径、条码与标签。
- **物品详情**：点击卡片 / 行弹出详情窗，展示图片、位置路径、数量、条码、标签、备注与时间，可跳转编辑或查看二维码。
- **批量导入**：上传 UTF-8 CSV（名称、位置、标签、条码、数量、备注），位置路径不存在时自动逐级创建；返回成功 / 失败明细。
- **批量删除**：选择模式下勾选多个物品一次性删除。
- **二维码标签**：物品 / 位置均可生成专属二维码；可批量排版打印标签页（浏览器打印或另存 PDF），也可单独下载 PNG。
- **位置布局图**：每个位置可上传平面 / 布局照片，主页按位置筛选后可查看。
- **扫码录入 / 扫码搜索**：调用浏览器原生 BarcodeDetector 识别条码 / 二维码（摄像头需 HTTPS 或 localhost 环境）。

## 技术栈

- 后端：Go 1.22，标准库 `net/http`，`golang.org/x/crypto/bcrypt` 密码哈希，`skip2/go-qrcode` 生成二维码。
- 前端：原生 HTML / CSS / JavaScript，无构建步骤、无外部 JS 依赖，编译时内嵌进二进制。
- 存储：`/data/data.json`（账号、位置、物品记录）+ `/data/uploads/`（图片）。

## 目录结构

```
family-inventory/
├── main.go            # HTTP 路由、接口处理器、鉴权中间件、图片 / 二维码接口
├── store.go           # JSON 文件存储、位置树、物品 / 账号业务逻辑
├── go.mod / go.sum
├── web/               # 内嵌前端：index.html / app.js / style.css
├── docker-compose.yml # NAS / Docker 编排示例
└── docs/部署说明.md    # 威联通 NAS 导入与部署说明（含校验和）
```

## 本地构建与运行

```bash
# 编译静态二进制（CGO 关闭）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o inventory ./...

# 运行（数据目录与端口可用环境变量覆盖）
INV_DATA=./data PORT=8080 ./inventory
```

浏览器访问 `http://localhost:8080`，首次打开先注册账号。

## Docker 部署

镜像基于 `linux/amd64` 单容器，入口 `/app/inventory`，暴露 `8080`，数据卷 `/data`。

```bash
docker run -d \
  --name family-inventory \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /share/Container/family-inventory/data:/data \
  family-inventory:1.0.0
```

或使用随附的 `docker-compose.yml`：

```bash
docker compose up -d
```

详细的 NAS 导入步骤、校验和与注意事项见 [`docs/部署说明.md`](docs/部署说明.md)。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `INV_DATA` | `/data` | 数据与图片存储目录 |
| `PORT` | `8080` | 容器内监听端口 |

## 安全说明

- 单账号设计：首个注册账号即管理员，之后不再开放注册；会话令牌保存在服务端内存，容器重启后需重新登录。
- 定位为家庭内网工具；如需外网访问，请通过反向代理叠加 HTTPS 与访问控制，不要直接暴露到公网。
- 二维码图片与上传图片由浏览器直接加载，对应接口未做令牌校验；数据读写接口均需登录。

## 许可

本项目仅作家庭内部使用，未指定开源许可证。
