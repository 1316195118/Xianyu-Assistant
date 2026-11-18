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
RUN chmod +x /app/xianyu-stat

# 三个目录分开挂载：
#   /data   数据库（DB_PATH）
#   /config 其他运行期文件，如附件截图（CONFIG_DIR）
#   /backup 备份（BACKUP_DIR）
ENV DB_PATH=/data/xianyu.db \
    CONFIG_DIR=/config \
    BACKUP_DIR=/backup \
    PORT=8080 \
    TZ=Asia/Shanghai

# 卷挂载点先建好，避免首次以只读或空卷启动时写入失败
RUN mkdir -p /data /config /backup
VOLUME ["/data", "/config", "/backup"]
EXPOSE 8080

ENTRYPOINT ["/app/xianyu-stat"]
