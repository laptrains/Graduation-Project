package router

import (
	"GopherAI/middleware/timing"

	"github.com/gin-gonic/gin"
)

func InitRouter() *gin.Engine {

	// 用 [timing] 取代 gin 自带的访问日志：两者内容重复，一次请求只保留一行
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(timing.Timing())

	enterRouter := r.Group("/api/v1")
	{
		// MVP：单人使用，无 JWT 鉴权，接口直接开放
		AIRouter(enterRouter.Group("/AI"))
	}

	return r
}
