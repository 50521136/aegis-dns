package api

import "testing"

// isAssetLikePath 决定「未命中的路径」是回落 index.html 还是返回 404。
//
// 判错的方向性后果不对称：
//   - 把资源误判成前端路由 → 浏览器拿到 200 + text/html 当 JS 执行，
//     被 nosniff 挡掉，白屏且控制台安静（最难查的那类故障）
//   - 把前端路由误判成资源 → 该路由直接 404，同样白屏
//
// 所以两侧都要有测试。真实场景：前端 base 配成相对路径 './' 时，
// 深层路由 /app/rules 下的 ./assets/index-xxx.js 会被解析成
// /app/assets/index-xxx.js，正是这里要挡住的东西。
func TestIsAssetLikePath(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
		why  string
	}{
		// 资源：应 404
		{"app/assets/index-BtSSsm14.js", true, "深层路由下被解析歪的资源路径"},
		{"assets/index-BtSSsm14.js", true, "正常资源路径"},
		{"assets/index-DFTTH401.css", true, "CSS"},
		{"assets/inter-latin-600-normal.woff2", true, "字体"},
		{"favicon.ico", true, "图标"},
		{"logo.svg", true, "SVG"},
		{"data.json", true, "JSON"},
		{"chunk.mjs", true, "ESM"},
		{"vendor.js.map", true, "source map"},
		{"UPPER.CSS", true, "扩展名大小写不敏感"},

		// 前端路由：应回落 index.html
		{"app", false, "一级路由"},
		{"app/rules", false, "二级路由"},
		{"app/settings", false, "二级路由"},
		{"login", false, "登录页"},
		{"admin", false, "管理后台"},
		{"", false, "根路径"},
		{"app/rules/detail", false, "三级路由"},

		// 边界：不该被当成资源
		{"app/v1.0", false, "路由里带点但不是已知资源后缀"},
		{"app/.hidden", false, "以点开头的段"},
		{"assets/", false, "目录形式"},
		{"noext", false, "无扩展名"},
	}
	for _, c := range cases {
		if got := isAssetLikePath(c.rel); got != c.want {
			t.Errorf("isAssetLikePath(%q) = %v，期望 %v（%s）", c.rel, got, c.want, c.why)
		}
	}
}
