// Package store 是 SQLite 数据访问层。
//
// 并发模型（文档 P3 单写者原则 + R4 写锁冲突对策）：
//
//	write 池 —— MaxOpenConns(1)，所有写操作串行通过它；
//	read  池 —— MaxOpenConns(N)，读不排队。
//
// SQLite 在 WAL 模式下本身是「多读一写」，但 Go 的 database/sql 连接池
// 会让多个 goroutine 同时抢写锁，表现为偶发 SQLITE_BUSY。把写收敛到单连接
// 是消除该问题最省事且不损失读吞吐的做法。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，CGO_ENABLED=0 也能交叉编译

	"github.com/50521136/aegis-dns/server/migrations"
)

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 是数据访问入口。
type Store struct {
	read  *sql.DB
	write *sql.DB
	path  string
}

// dsn 构造 modernc.org/sqlite 的连接串。
//
// 关键 PRAGMA 在连接建立时通过 _pragma 注入，保证池里每一条新连接都带上，
// 而不是只在启动时对某一条连接设置（后者是常见 bug：池扩容后新连接没有
// busy_timeout，于是并发下又开始报 database is locked）。
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=journal_mode(WAL)"
}

// Open 打开数据库、设置 PRAGMA 并执行迁移。
func Open(path string) (*Store, error) {
	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)

	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("打开只读连接池失败: %w", err)
	}
	read.SetMaxOpenConns(8)
	read.SetMaxIdleConns(8)
	read.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// journal_mode 是持久化设置，但对新库需要显式触发一次。
	var mode string
	if err := write.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		write.Close()
		read.Close()
		return nil, fmt.Errorf("设置 WAL 模式失败: %w", err)
	}

	s := &Store{read: read, write: write, path: path}
	if err := s.migrate(ctx); err != nil {
		write.Close()
		read.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭两个连接池。
func (s *Store) Close() error {
	var errs []error
	if s.read != nil {
		errs = append(errs, s.read.Close())
	}
	if s.write != nil {
		errs = append(errs, s.write.Close())
	}
	return errors.Join(errs...)
}

// Path 返回数据库文件路径。
func (s *Store) Path() string { return s.path }

// migrate 按文件名顺序执行尚未应用的迁移。
//
// 每个迁移在独立事务内执行，失败即回滚并中止启动 —— 半应用的 schema
// 比启动失败危险得多。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("初始化 meta 表失败: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("读取迁移目录失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	current, _ := s.GetMeta(ctx, "schema_version")
	currentVer, _ := strconv.Atoi(current)

	for _, name := range names {
		ver, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if ver <= currentVer {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("读取迁移 %s 失败: %w", name, err)
		}
		tx, err := s.write.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("开启迁移事务失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行迁移 %s 失败: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES ('schema_version', ?)
			ON CONFLICT(k) DO UPDATE SET v = excluded.v`, strconv.Itoa(ver)); err != nil {
			tx.Rollback()
			return fmt.Errorf("记录迁移版本失败: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交迁移 %s 失败: %w", name, err)
		}
	}
	return nil
}

// migrationVersion 从 "0001_init.sql" 中解析出版本号 1。
func migrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, fmt.Errorf("迁移文件名 %q 不符合 <版本号>_<描述>.sql 约定", name)
	}
	ver, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, fmt.Errorf("迁移文件名 %q 的版本号无法解析: %w", name, err)
	}
	return ver, nil
}

// tx 在写事务中执行 fn，自动提交或回滚。
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	t, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}

// boolInt 把 bool 转成 SQLite 的 0/1。
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
