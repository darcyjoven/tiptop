.PHONY: all build build-ps test test-race vet fmt lint clean help

GO ?= go

# ---- 平台分支 ----
# 与 bdl-lsp 相同的 CGO 处理：Windows 下需本机 MinGW gcc，
# 且用 GOENV=off 屏蔽可能被其它 SDK 改写的全局 go env。
ifeq ($(OS),Windows_NT)
  WIN_ARCH ?= amd64
  WIN_CC   ?= gcc
  WIN_CXX  ?= g++
  WIN_GOPROXY ?= https://goproxy.cn,direct
  export GOENV := off
  export GOOS := windows
  export GOARCH := $(WIN_ARCH)
  export CGO_ENABLED := 1
  export CC := $(WIN_CC)
  export CXX := $(WIN_CXX)
  export GOPROXY := $(WIN_GOPROXY)
  BIN := artifacts/bin/tiptop.exe
  EXE := .exe
  MSG_BUILT := built
else
  BIN := artifacts/bin/tiptop
  EXE :=
  MSG_BUILT := 已生成
endif

all: build

## build: 构建本地二进制到 artifacts/bin/tiptop（Windows 为 tiptop.exe）
build:
	$(GO) build -trimpath -o $(BIN) .
	@echo "$(MSG_BUILT) $(BIN)"

## build-ps: Windows PowerShell 构建（无需 make）
build-ps:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1

## test: 全量单元测试
test:
	$(GO) test ./...

## test-race: 竞态检测
test-race:
	$(GO) test -race ./...

## vet: go vet
vet:
	$(GO) vet ./...

## fmt: 格式化
fmt:
	$(GO) fmt ./...

## lint: vet + gofmt 检查
lint: vet
	@test -z "$$(gofmt -l .)" || { echo "以下文件未格式化："; gofmt -l .; exit 1; }
	@echo "格式检查通过"

## clean: 清理构建产物
clean:
	rm -rf artifacts/bin

## help: 列出目标
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
