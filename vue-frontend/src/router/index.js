import { createRouter, createWebHistory } from 'vue-router'
import Menu from '../views/Menu.vue'
import AIChat from '../views/AIChat.vue'

const routes = [
  {
    path: '/',
    redirect: '/menu'
  },
  {
    path: '/menu',
    name: 'Menu',
    component: Menu
  },
  {
    path: '/ai-chat',
    name: 'AIChat',
    component: AIChat
  }
]

const router = createRouter({
  history: createWebHistory(process.env.BASE_URL),
  routes
})

export default router
