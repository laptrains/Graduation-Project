package main

import (
	"GopherAI/common/messagewriter"
	"GopherAI/common/mysql"
	"GopherAI/common/redis"
	"GopherAI/config"
	"GopherAI/router"
	"fmt"
	"log"
)

func StartServer(addr string, port int) error {
	r := router.InitRouter()
	//服务器静态资源路径映射关系，这里目前不需要
	// r.Static(config.GetConfig().HttpFilePath, config.GetConfig().MusicFilePath)
	return r.Run(fmt.Sprintf("%s:%d", addr, port))
}

func main() {
	conf := config.GetConfig()
	host := conf.MainConfig.Host
	port := conf.MainConfig.Port
	//初始化mysql
	if err := mysql.InitMysql(); err != nil {
		log.Println("InitMysql error , " + err.Error())
		return
	}
	// 会话记忆改为首次访问时懒加载（摘要 + 近期窗口），启动时不再全量加载消息

	//初始化redis
	redis.Init()
	log.Println("redis init success  ")

	// 启动消息批量写入器（进程内异步批量落库）
	messagewriter.GetWriter()
	log.Println("messagewriter init success  ")

	err := StartServer(host, port) // 启动 HTTP 服务
	if err != nil {
		panic(err)
	}
}
