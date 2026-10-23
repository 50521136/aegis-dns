package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("读失败:", err)
		return
	}
	var snap model.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		fmt.Println("解析失败:", err)
		return
	}
	uid := os.Args[2]
	var target *model.UserSnap
	for i := range snap.Users {
		if snap.Users[i].UserID == uid {
			target = &snap.Users[i]
		}
	}
	if target == nil {
		fmt.Println("无该用户")
		return
	}
	// 全局层：把 global.rules + global sub 一起带上
	gl := rules.BuildGlobal(&snap.Global)

	rs, err := rules.BuildWithGlobal(gl, rules.Layers{
		UserRules: target.Rules,
		SubAllow:  target.SubAllow,
		SubBlock:  target.SubBlock,
	})
	if err != nil {
		fmt.Println("构建失败:", err)
		return
	}
	fmt.Println("=== 带全局层后的匹配 ===")
	for _, d := range []string{
		"t.apkpure.net", "tapi.apkpure.net", "apkpure.com",
		"github.com", "github.io", "github.dev",
	} {
		fmt.Printf("  %-26s -> %s\n", d, rs.Match(d, 1).Action)
	}
}
