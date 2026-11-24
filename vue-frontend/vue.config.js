module.exports = {
  chainWebpack: config => {
    // 项目路径含括号（GopherAI-v2(1)），默认的绝对路径 ignore 会被 fast-glob 当作
    // extglob 语法解析而失效，导致 public/index.html 被重复拷贝、与 html-webpack-plugin 冲突。
    // 改为相对 glob，与路径特殊字符无关。
    config.plugin('copy').tap(args => {
      args[0].patterns[0].globOptions.ignore = ['**/.DS_Store', '**/index.html']
      return args
    })
  },
  devServer: {
    port: 8080,
    proxy: {
      '/api': {
        target: 'http://localhost:9090',
        changeOrigin: true,
        pathRewrite: {
          '^/api': '/api/v1'
        }
      }
    }
  }
}