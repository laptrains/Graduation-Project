import axios from 'axios'

const api = axios.create({
  baseURL: '/api', // 使用代理路径，开发环境会自动代理到后端
  timeout: 0  //不启用超时机制
})

export default api
