import { useState } from 'react'
import { App, Button, Card, Form, Input, Typography } from 'antd'
import { LockOutlined } from '@ant-design/icons'
import { get, setToken } from '../api'
import type { StatusView } from '../types'

interface Props {
  onLogin: () => void
}

// 登录页只做本地令牌校验：令牌随每个请求以 Bearer 发送。
export default function LoginCard({ onLogin }: Props) {
  const { message } = App.useApp()
  const [loading, setLoading] = useState(false)

  async function submit(values: { token: string }) {
    setLoading(true)
    setToken(values.token.trim())
    try {
      await get<StatusView>('/api/v1/status')
      onLogin()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-wrap">
      <Card className="login-card">
        <Typography.Title level={4} style={{ marginTop: 0 }}>
          模板服务器
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 20 }}>
          输入管理令牌以配置模板、映射未知模板并发布分发。
        </Typography.Paragraph>
        <Form layout="vertical" onFinish={submit}>
          <Form.Item
            name="token"
            label="ADMIN_TOKEN"
            rules={[{ required: true, message: '请输入管理令牌' }]}
          >
            <Input.Password prefix={<LockOutlined />} placeholder="管理令牌" autoFocus />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
