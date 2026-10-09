// Package migrations 以 embed.FS 形式携带 SQL 迁移脚本。
//
// 放在 server/migrations 而不是 internal/store 下，是为了让运维能直接
// 在仓库里读到、diff、审计 DDL；store 通过本包读取，不依赖运行时文件系统。
package migrations

import "embed"

// FS 内嵌 migrations/ 目录下的全部 .sql 文件，按文件名排序执行。
//
//go:embed *.sql
var FS embed.FS
