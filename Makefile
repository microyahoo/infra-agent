.PHONY: build test clean install docker-build docker-push

# 变量定义
BINARY_NAME=infra-agent
VERSION?=dev
GIT_COMMIT=$(shell git rev-parse --short HEAD)
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-X github.com/microyahoo/infra-agent/cmd.Version=$(VERSION) \
                  -X github.com/microyahoo/infra-agent/cmd.GitCommit=$(GIT_COMMIT) \
                  -X github.com/microyahoo/infra-agent/cmd.BuildTime=$(BUILD_TIME)"

# 默认目标
all: build

# 构建
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) .

# 构建所有平台
build-all:
	@echo "Building for all platforms..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe .

# 运行测试
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

# 测试覆盖率
coverage:
	@echo "Generating coverage report..."
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# 清理
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -f coverage.out coverage.html

# 安装
install:
	@echo "Installing $(BINARY_NAME)..."
	go install $(LDFLAGS) .

# 代码检查
lint:
	@echo "Running linter..."
	golangci-lint run ./...

# 格式化代码
fmt:
	@echo "Formatting code..."
	go fmt ./...
	goimports -w .

# 更新依赖
deps:
	@echo "Updating dependencies..."
	go mod tidy
	go mod download

# Docker 构建
docker-build:
	@echo "Building Docker image..."
	docker build -t microyahoo/$(BINARY_NAME):$(VERSION) .
	docker tag microyahoo/$(BINARY_NAME):$(VERSION) microyahoo/$(BINARY_NAME):latest

# Docker 推送
docker-push:
	@echo "Pushing Docker image..."
	docker push microyahoo/$(BINARY_NAME):$(VERSION)
	docker push microyahoo/$(BINARY_NAME):latest

# 运行
run:
	go run . analyze --explain

# 帮助信息
help:
	@echo "Available targets:"
	@echo "  build        - Build the binary"
	@echo "  build-all    - Build for all platforms"
	@echo "  test         - Run tests"
	@echo "  coverage     - Generate test coverage report"
	@echo "  clean        - Clean build artifacts"
	@echo "  install      - Install the binary"
	@echo "  lint         - Run linter"
	@echo "  fmt          - Format code"
	@echo "  deps         - Update dependencies"
	@echo "  docker-build - Build Docker image"
	@echo "  docker-push  - Push Docker image"
	@echo "  run          - Run the application"
.PHONY: test-basic test-analyze test-all

# 基础测试
test-basic:
	@echo "运行基础测试..."
	go test -v ./pkg/common/...
	go test -v ./pkg/analyzer/...

# 分析器测试
test-analyze:
	@echo "测试分析器功能..."
	./test.sh

# 运行所有测试
test-all: test-basic test-analyze
	@echo "所有测试完成！"

.PHONY: demo
demo:
	@echo "======================================"
	@echo "Infra Agent 演示"
	@echo "======================================"
	@./bin/infra-agent version
	@echo ""
	@echo "配置示例："
	@echo "  export OPENAI_API_KEY='sk-...'"
	@echo "  export FEISHU_WEBHOOK_URL='https://...'"
	@echo ""
	@echo "使用示例："
	@echo "  ./bin/infra-agent analyze --explain"
	@echo "  ./bin/infra-agent analyze --explain --auto-diagnose --notify"
	@echo "  ./bin/infra-agent patrol start --interval 30m"
