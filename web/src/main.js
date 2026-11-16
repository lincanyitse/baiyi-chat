import { mount } from 'svelte'
import './app.css'
import 'virtual:uno.css'
import './markdown.css'
import App from './App.svelte'
import { restoreAppearance } from './lib/appearance.js'

// 顺序：变量 → 原子类 → markdown 覆盖。
// 先恢复外观再挂载：晚一步会闪一下默认主题。
restoreAppearance()

const app = mount(App, {
  target: document.getElementById('app'),
})

export default app
