package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// userCols 是 users 表所有 SELECT 共用的列清单，避免各处手写列名漂移。
const userCols = `id, username, email, password_hash, role, client_id, enabled, created_at`

func scanUser(sc interface{ Scan(...any) error }) (*model.User, error) {
	var u model.User
	var enabled int
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.ClientID, &enabled, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled != 0
	return &u, nil
}

// CreateUser 创建用户并分配一个唯一的 client_id。
//
// client_id 的唯一性靠 UNIQUE 约束 + 冲突重试保证：先尝试插入，撞了就换一个。
// 比「先 SELECT 再 INSERT」更可靠（后者在并发下有 TOCTOU 窗口）。
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash, role string) (*model.User, error) {
	username = strings.TrimSpace(username)
	if role == "" {
		role = model.RoleUser
	}
	now := time.Now().Unix()

	for attempt := 0; attempt < 5; attempt++ {
		u := &model.User{
			ID:           id.New(id.PrefixUser),
			Username:     username,
			Email:        email,
			PasswordHash: passwordHash,
			Role:         role,
			ClientID:     id.NewClientID(),
			Enabled:      true,
			CreatedAt:    now,
		}
		_, err := s.write.ExecContext(ctx,
			`INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.ClientID, boolInt(u.Enabled), u.CreatedAt)
		if err == nil {
			return u, nil
		}
		if isUniqueViolation(err, "users.client_id") {
			continue // client_id 撞车，换一个重试
		}
		if isUniqueViolation(err, "users.username") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	return nil, errors.New("连续 5 次未能分配唯一 client_id，请检查随机数源")
}

// ErrUsernameTaken 表示用户名已被占用。
var ErrUsernameTaken = errors.New("用户名已存在")

// isUniqueViolation 判断错误是否为指定列上的唯一约束冲突。
//
// modernc.org/sqlite 的错误文本形如：
//
//	UNIQUE constraint failed: users.client_id
//
// 这里同时匹配约束名与通用文本，避免驱动措辞变化导致判断失效。
func isUniqueViolation(err error, constraint string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "UNIQUE constraint failed") {
		return false
	}
	return strings.Contains(msg, constraint)
}

// GetUserByUsername 按用户名查询。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return u, nil
}

// GetUserByID 按主键查询。
func (s *Store) GetUserByID(ctx context.Context, uid string) (*model.User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, uid)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return u, nil
}

// GetUserByClientID 按 client_id 查询（用于反查子域名归属，管理接口用）。
func (s *Store) GetUserByClientID(ctx context.Context, cid string) (*model.User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE client_id = ?`, cid)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return u, nil
}

// ListUsers 分页列出用户，可按用户名/邮箱模糊搜索。
func (s *Store) ListUsers(ctx context.Context, q string, limit, offset int) ([]*model.User, int, error) {
	where := ""
	args := []any{}
	if q != "" {
		where = ` WHERE username LIKE ? OR email LIKE ? OR client_id LIKE ?`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}

	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计用户数失败: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+userCols+` FROM users`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出用户失败: %w", err)
	}
	defer rows.Close()

	var out []*model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// CountUsers 返回用户总数。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsersAll 返回全部用户（构建快照用，不分页）。
//
// 与 ListUsers 分开：快照构建必须看到所有用户，任何分页截断都会让
// 部分租户的规则静默失效。
func (s *Store) ListUsersAll(ctx context.Context) ([]*model.User, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("列出全部用户失败: %w", err)
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserFields 更新用户的可变字段。传 nil 表示不改该字段。
func (s *Store) UpdateUserFields(ctx context.Context, uid string, enabled *bool, role *string, email *string) error {
	sets := []string{}
	args := []any{}
	if enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolInt(*enabled))
	}
	if role != nil {
		sets = append(sets, "role = ?")
		args = append(args, *role)
	}
	if email != nil {
		sets = append(sets, "email = ?")
		args = append(args, *email)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, uid)
	res, err := s.write.ExecContext(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("更新用户失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword 更新密码哈希。
func (s *Store) UpdateUserPassword(ctx context.Context, uid, hash string) error {
	res, err := s.write.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, uid)
	if err != nil {
		return fmt.Errorf("更新密码失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser 删除用户（外键 CASCADE 会清掉其规则、订阅、IP 归属、令牌）。
func (s *Store) DeleteUser(ctx context.Context, uid string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid)
	if err != nil {
		return fmt.Errorf("删除用户失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureAdmin 在没有任何管理员时创建初始管理员。
//
// 幂等：已经存在管理员就什么都不做，因此可以每次启动都调用。
// 返回是否新建了账号，调用方据此打印一次性提示。
func (s *Store) EnsureAdmin(ctx context.Context, username, email, passwordHash string) (bool, error) {
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, model.RoleAdmin).Scan(&n); err != nil {
		return false, fmt.Errorf("检查管理员失败: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if _, err := s.CreateUser(ctx, username, email, passwordHash, model.RoleAdmin); err != nil {
		return false, err
	}
	return true, nil
}

// === 来源 IP 归属 ===

// ListUserIPs 返回某个用户的全部 CIDR。
func (s *Store) ListUserIPs(ctx context.Context, uid string) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT cidr FROM user_ips WHERE user_id = ? ORDER BY cidr`, uid)
	if err != nil {
		return nil, fmt.Errorf("查询来源 IP 失败: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddUserIP 为某个用户登记一条来源 CIDR。
func (s *Store) AddUserIP(ctx context.Context, uid, cidr string) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO user_ips (user_id, cidr) VALUES (?, ?) ON CONFLICT(user_id, cidr) DO NOTHING`, uid, cidr)
	if err != nil {
		return fmt.Errorf("登记来源 IP 失败: %w", err)
	}
	return nil
}

// DeleteUserIP 删除一条来源 CIDR。
func (s *Store) DeleteUserIP(ctx context.Context, uid, cidr string) error {
	_, err := s.write.ExecContext(ctx, `DELETE FROM user_ips WHERE user_id = ? AND cidr = ?`, uid, cidr)
	return err
}

// AllUserIPs 返回 user_id -> []cidr 的完整映射，供快照构建使用。
func (s *Store) AllUserIPs(ctx context.Context) (map[string][]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT user_id, cidr FROM user_ips ORDER BY user_id, cidr`)
	if err != nil {
		return nil, fmt.Errorf("查询全部来源 IP 失败: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var uid, cidr string
		if err := rows.Scan(&uid, &cidr); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], cidr)
	}
	return out, rows.Err()
}
