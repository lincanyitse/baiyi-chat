# 白泽 Web 前端

Svelte 5 + Vite。构建产物直接落到 `internal/channel/web/dist`，由 Go 的
`go:embed` 打包进二进制——`go:embed` 不能引用包目录之外的路径，所以
`vite.config.js` 里 `build.outDir` 指向了反方向的 `../internal/channel/web/dist`。

## 开发

```bash
npm install
npm run dev      # http://localhost:5173，/api 代理到 127.0.0.1:8787
```

开发时需要另一个终端跑后端：

```bash
../bin/baiyi.exe --config ../data/config.toml serve
```

## 构建

```bash
npm run build    # 产物落到 internal/channel/web/dist
```

或从仓库根目录 `make web`。

## 结构

```
src/
  main.js               挂载入口
  App.svelte            主布局：侧栏 + 消息列表 + 输入框
  app.css               主题变量（组件只引用变量，不写死颜色）
  lib/
    api.js              HTTP 客户端，统一错误处理
    sse.js              SSE 订阅 + StreamAssembler（把事件归并成块）
    store.svelte.js     全局状态（$state）与会话动作
  components/
    SessionList.svelte  会话侧栏
    Message.svelte      消息气泡
    ThinkingBlock.svelte 可折叠的思考过程
    Composer.svelte     输入框与发送/停止按钮
```

## 为什么要 StreamAssembler

服务端推的是细粒度事件（`text_delta` 逐字），前端要的是「消息里有哪些
块、每块写到哪儿了」。归并逻辑集中在这一个类里，组件只负责渲染。

`turn_finished` 到达后不信任本地的归并结果，而是重新拉一次
`/api/sessions/{id}/messages` 整体替换——断线期间丢过事件也能对齐到
数据库的最终状态。
