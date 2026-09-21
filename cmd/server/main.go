// Command server 启动页脉探针 Web 服务。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"pagepulse/internal/store"
	"pagepulse/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5246", "监听地址")
	dataDir := flag.String("data", "data", "数据目录(SQLite 与原件)")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("打开数据目录失败: %v", err)
	}
	defer st.Close()

	srv := web.New(st)
	fmt.Printf("页脉探针已启动: http://%s\n", *addr)
	fmt.Printf("数据目录: %s（原件只读保存，绝不重写）\n", *dataDir)
	if err := http.ListenAndServe(*addr, srv.Mux()); err != nil {
		log.Fatal(err)
	}
}
