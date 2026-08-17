// 起 echdoh + 验证 /admin 路由
package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/anglesgirl/ech-proxy-go/mobile/echdoh"
)

func main() {
	cert, _ := os.ReadFile("/root/work/echdoh-certs/doh-fullchain.pem")
	key, _ := os.ReadFile("/root/work/echdoh-certs/doh-key.pem")
	if err := echdoh.Start("127.0.0.1:18444", string(cert), string(key),
		"https://pieqllv9i7.cloudflare-gateway.com/dns-query"); err != nil {
		fmt.Println("Start err:", err)
		return
	}
	time.Sleep(1500 * time.Millisecond)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	// 验证 /admin 三端点
	for _, path := range []string{"/admin", "/admin/api/status", "/admin/api/logs"} {
		url := "https://127.0.0.1:18444" + path
		resp, err := client.Get(url)
		if err != nil {
			fmt.Println(path, "FAIL:", err)
			continue
		}
		buf := make([]byte, 200)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		fmt.Printf("%-20s HTTP %d: %s\n", path, resp.StatusCode, string(buf[:n]))
	}
}
