# 闲鱼售卖统计 —— 运行镜像
# 二进制是 CGO 静态编译的（build.sh 产出），所以用 alpine 即可，体积小。
FROM alpine:3.21

# ca-certificates：调用闲鱼/阿里 mtop 接口必须校验 HTTPS 证书
# tzdata：日志和账单时间按 Asia/Shanghai 输出
RUN apk add --no-cache ca-certificates tzdata \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app
COPY xianyu-stat-linux-amd64 /app/xianyu-stat

# 三个目录分开挂载：
#   /data   数据库（DB_PATH）
#   /config 其他运行期文件，如附件截图（CONFIG_DIR）
#   /backup 备份（BACKUP_DIR）
# Compose 只需覆盖 PORT 和 TZ；下面这些路径与默认端口已经内置。
ENV DB_PATH=/data/xianyu.db \
    CONFIG_DIR=/config \
    BACKUP_DIR=/backup \
    PORT=8080 \
    TZ=Asia/Shanghai

# 以非 root 用户运行；在线更新需要替换 /app 下的二进制，因此运行用户还要拥有 /app。
RUN addgroup -S xianyu \
    && adduser -S -G xianyu -h /app -s /sbin/nologin xianyu \
    && mkdir -p /data /config /backup \
    && chmod +x /app/xianyu-stat \
    && chown -R xianyu:xianyu /app /data /config /backup
VOLUME ["/data", "/config", "/backup"]
EXPOSE 8080
USER xianyu

ENTRYPOINT ["/app/xianyu-stat"]
