import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { HeroUIProvider } from '@heroui/react'
import './index.css'
import './theme/fusion.css'
import './theme/app.css'
import { App } from './App'
import { AuthProvider } from './auth/AuthContext'

// brain 把控制台静态产物托管在 /app（docs/api.md §17.1）。
// 开发期仍可用 Vite 服务器；basename 与托管路径对齐。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <HeroUIProvider>
      <BrowserRouter basename={import.meta.env.BASE_URL}>
        <AuthProvider>
          <App />
        </AuthProvider>
      </BrowserRouter>
    </HeroUIProvider>
  </StrictMode>,
)
