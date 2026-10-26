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

# 复制配置示例，供用户参考。
# 程序只从 --config、~/.infra-agent.yaml 与 ./.infra-agent.yaml 读取配置，
# 因此这份文件不会被自动加载——需要时复制它并修改。
COPY config/doctor.example.yaml /etc/infra-agent/doctor.example.yaml

# 创建数据目录
RUN mkdir -p /var/lib/infra-agent/history

# 暴露端口
EXPOSE 8080 8081

# 设置时区
ENV TZ=Asia/Shanghai

ENTRYPOINT ["infra-agent"]
CMD ["serve"]
