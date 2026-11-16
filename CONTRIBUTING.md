# 参与贡献

欢迎修缺陷和补文档。新功能请先开 Issue，说明它解决的使用场景。

## 环境

- Go 1.26
- Node.js 22（Vite 8 需要 20.19+ 或 22.12+，Node 18 跑不起来）

## 提交前

```bash
npm --prefix web test
npm --prefix web run build
CGO_ENABLED=0 go test ./...
go vet ./...
bash scripts/check-layering.sh
gofmt -l .
```

`gofmt -l .` 有输出就先格式化。分层检查失败时，不要为了通过去改 `scripts/check-layering.sh` 的允许列表，先说明为什么这层依赖是合理的。

## 提交说明

用中文，一行写清原因，例如 `fix: 零配置启动不再指向不存在的模型`。不要提交 `scratch/`、数据库、`dist` 里的压缩包，或任何密钥。
