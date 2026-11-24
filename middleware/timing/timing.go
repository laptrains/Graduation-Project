// Package timing HTTP 接口耗时统计中间件
package timing

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// slowThreshold 慢请求阈值，耗时超过该值时日志带 [SLOW] 标记，便于观察模型调用耗时异常
var slowThreshold = 10 * time.Second

// Timing 记录请求的方法、路径、状态码与总耗时。
// 耗时覆盖 handler 全流程，对 SSE 流式接口会一直统计到流结束，可据此观察模型调用时长。
// 现挂在全局以替代 gin 默认访问日志，AI 接口会多计入 jwt 解析耗时（微秒级，可忽略）。
func Timing() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		logRequest(c, time.Since(start))
	}
}

// logRequest 输出一条耗时日志
func logRequest(c *gin.Context, cost time.Duration) {
	status := c.Writer.Status()
	if status == 0 {
		status = http.StatusOK
	}
	// FullPath 为路由模板（如 /api/v1/AI/chat/send），避免高基数；未匹配到路由时为空
	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}

	if cost >= slowThreshold {
		log.Printf("[timing][SLOW] ip=%s method=%s path=%s status=%d cost=%dms",
			c.ClientIP(), c.Request.Method, path, status, cost.Milliseconds())
		return
	}
	log.Printf("[timing] ip=%s method=%s path=%s status=%d cost=%dms",
		c.ClientIP(), c.Request.Method, path, status, cost.Milliseconds())
}
