# Traffic Monitor

`Traffic Monitor` 是一个用于 Mihomo 的轻量流量监控镜像。

容器启动后会持续采集 Mihomo 的连接流量，并在 `8080` 端口提供 Web 页面，方便查看设备、主机、代理维度的流量数据。

## 怎么用

```bash
docker run -d \
  --name traffic-monitor \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MIHOMO_URL=http://host.docker.internal:9090 \
  -e MIHOMO_SECRET=your-secret \
  -v "$(pwd)/data:/data" \
  zhf883680/clash-traffic-monitor:latest
```

启动后访问：

```text
http://localhost:8080/
```

## 常用环境变量

| 变量名 | 默认值 | 说明 |
| --- | --- | --- |
| `MIHOMO_URL` | `http://127.0.0.1:9090` | Mihomo Controller 地址 |
| `MIHOMO_SECRET` | 空 | Mihomo Bearer Token |
| `TRAFFIC_MONITOR_LISTEN` | `:8080` | 服务监听地址 |

## 存储说明

- 容器内数据库文件固定为 `/data/traffic_monitor.db`，因此持久化时应挂载到 `/data`。
- 本地直接运行二进制时，默认路径会切换到 `./data/traffic_monitor.db`，不会去写根目录 `/data`。
- 运行时只持久化 30 天分钟级聚合数据。
- 最近最多 10 分钟的数据先保存在内存里，按批次刷盘，以减少磁盘 IO。

## 页面预览

![Traffic Monitor 页面预览](https://raw.githubusercontent.com/zhf883680/clash-traffic-monitor/main/readmeImg/image.png)
