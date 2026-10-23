package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// 用线上真实快照验证某个用户的规则命中结果。
func main() {
	path := os.Args[1]
	uid := os.Args[2]
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("读快照失败:", err)
		return
	}
	var snap model.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		fmt.Println("解析快照失败:", err)
		return
	}

	var target *model.UserSnap
	for i := range snap.Users {
		if snap.Users[i].UserID == uid {
			target = &snap.Users[i]
			break
		}
	}
	if target == nil {
		fmt.Println("快照里没有该用户")
		return
	}
	fmt.Printf("用户: %s  client_id=%s  rules=%d  sub_allow=%d  sub_block=%d\n\n",
		target.UserID, target.ClientID, len(target.Rules), len(target.SubAllow), len(target.SubBlock))

	rs, err := rules.BuildWithGlobal(nil, rules.Layers{
		UserRules: target.Rules,
		SubAllow:  target.SubAllow,
		SubBlock:  target.SubBlock,
	})
	if err != nil {
		fmt.Println("构建规则集失败:", err)
		return
	}

	domains := []string{
		"github.com", "api.github.com", "alive.github.com", "github.githubassets.com",
		"raw.githubusercontent.com", "avatars.githubusercontent.com",
		"objects.githubusercontent.com", "user-images.githubusercontent.com",
		"camo.githubusercontent.com", "github.io", "github.dev",
		"apkpure.com", "t.apkpure.net", "tapi.apkpure.net", "www.apkmirror.com",
		"sinaimg.cn", "wx1.sinaimg.cn",
	}
	fmt.Println("=== 实际匹配结果 ===")
	for _, d := range domains {
		dec := rs.Match(d, 1) // qtype 1 = A
		fmt.Printf("  %-40s -> %s\n", d, dec.Action)
	}
}
