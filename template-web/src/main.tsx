import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import Root from './App'
import './styles.css'

// 设计 token 与主管理后台（DESIGN.md）保持一致：白底、细边框、行动蓝。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: '#056dff',
          colorLink: '#193cb8',
          colorBgLayout: '#fbfbfb',
          colorBorderSecondary: '#f2f2f2',
          borderRadius: 8,
          fontFamily:
            "Inter, system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif",
        },
      }}
    >
      <AntApp>
        <Root />
      </AntApp>
    </ConfigProvider>
  </StrictMode>,
)
