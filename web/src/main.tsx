import React from 'react'
import ReactDOM from 'react-dom/client'

// 珍珠玻璃主题使用系统字体栈（SF Pro / 苹方 / Noto Sans SC / 微软雅黑），
// 不再打包 Inter —— 少约 300KB 字体文件，且中文渲染更贴合系统。
import './styles/globals.css'
import App from './App'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
