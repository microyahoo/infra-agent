# 构建阶段
FROM golang:1.21-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git make

# 复制 go mod 文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN make build

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /build/bin/infra-agent /usr/local/bin/infra-agent

# 复制默认配置
COPY config/default.yaml /etc/infra-agent/config.yaml

# 创建数据目录
RUN mkdir -p /var/lib/infra-agent/history

# 暴露端口
EXPOSE 8080 8081

# 设置时区
ENV TZ=Asia/Shanghai

ENTRYPOINT ["infra-agent"]
CMD ["serve"]
