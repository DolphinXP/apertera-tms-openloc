# 开发与部署速查

以下命令均在 `deploy/` 目录下执行。postgres / redis / seaweedfs / parser 及其数据卷在两种模式间共用，切换不会丢数据。

## 开发模式（热重载）

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

- 访问地址不变：`http://localhost:8080` 或 `ai-innovation-tms.at-ai.systems`
  - 经域名访问时，nginx 需开启 `http2` 并转发 WebSocket 升级头，否则模块加载极慢、热更新失效（本机 `/etc/nginx/sites-available/ai-innovation-tms.at-ai.systems` 已配置）
  - 其他域名访问会被 Vite 拦截，需在 `deploy/.env` 中设置 `DEV_ALLOWED_HOSTS=域名1,域名2` 后重启 `web`
- 后端：air 监听 Go 代码，保存后自动编译、执行 migration、重启（约 7 秒）
- 前端：Vite dev server，保存后浏览器即时更新（HMR）

查看日志（编译错误也在这里）：

```bash
docker logs -f deploy-app-1   # 后端
docker logs -f deploy-web-1   # 前端
```

依赖变更：

- `go.mod` 变更：下次编译时自动下载，无需操作
- `web/package-lock.json` 变更：`docker restart deploy-web-1`（重启时自动 `npm ci`）

> `touch` 只改时间戳，不会触发重新编译；需实际修改文件内容。

## 镜像模式（构建正式镜像）

```bash
docker compose up -d --build --remove-orphans
```

`--remove-orphans` 用于清理开发模式下的 `web` 容器。提交或上线前，建议用此模式完整验证一次。

## 相关文件

| 文件 | 作用 |
| --- | --- |
| `deploy/docker-compose.dev.yml` | 开发模式 override：`app` 改为 air 热重载（后端 :8081），新增 `web`（Vite :8080） |
| `deploy/air.dev.toml` | air 配置：监听范围、编译命令、每次重启前执行 migration |
