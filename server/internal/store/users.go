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
const userCols = `id, username, email, password_hash, role, client_id, enabled, use_global_rules, id_reset_count, rules_enabled, subs_enabled, custom_id_used, member_until, invited_by, created_at, custom_max_rules, custom_max_subs, custom_daily_queries, log_cleared_at, tier_id`

func scanUser(sc interface{ Scan(...any) error }) (*model.User, error) {
	var u model.User
	var enabled, useGlobal, rulesOn, subsOn, customUsed int
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.ClientID,
		&enabled, &useGlobal, &u.IDResetCount, &rulesOn, &subsOn, &customUsed,
		&u.MemberUntil, &u.InvitedBy, &u.CreatedAt,
		&u.CustomMaxRules, &u.CustomMaxSubs, &u.CustomDailyQueries, &u.LogClearedAt, &u.TierID)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled != 0
	u.UseGlobalRules = useGlobal != 0
	u.RulesEnabled = rulesOn != 0
	u.SubsEnabled = subsOn != 0
	u.CustomIDUsed = customUsed != 0
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
			// 默认叠加全局规则层，用户可在设置里自行关闭。
			UseGlobalRules: true,
			// 新用户默认允许建规则与订阅；管理员可以单独关掉。
			RulesEnabled: true,
			SubsEnabled:  true,
			CreatedAt:    now,
		}
		_, err := s.write.ExecContext(ctx,
			// 列名单独写死，不复用 userCols：
			// userCols 是给 SELECT 用的，以后加字段只改一处就会让
			// 「N values for M columns」当场炸掉（已经踩过一次）。
			`INSERT INTO users (id, username, email, password_hash, role, client_id, enabled,
			                    use_global_rules, id_reset_count, rules_enabled, subs_enabled,
			                    custom_id_used, member_until, invited_by, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.ClientID,
			boolInt(u.Enabled), boolInt(u.UseGlobalRules), u.IDResetCount,
			boolInt(u.RulesEnabled), boolInt(u.SubsEnabled), boolInt(u.CustomIDUsed),
			u.MemberUntil, u.InvitedBy, u.CreatedAt)
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
// GetUser 是 GetUserByID 的短别名，供配额换算等高频调用点使用。
func (s *Store) GetUser(ctx context.Context, uid string) (*model.User, error) {
	return s.GetUserByID(ctx, uid)
}

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
// GetUserByEmail 按邮箱查用户（找回密码用）。查不到返回 (nil, nil)。
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE LOWER(email) = ? AND email != ''`, email)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return u, nil
}

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
func (s *Store) UpdateUserFields(ctx context.Context, uid string, enabled *bool, role *string, email *string, useGlobalRules, rulesEnabled, subsEnabled *bool) error {
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
	if useGlobalRules != nil {
		sets = append(sets, "use_global_rules = ?")
		args = append(args, boolInt(*useGlobalRules))
	}
	if rulesEnabled != nil {
		sets = append(sets, "rules_enabled = ?")
		args = append(args, boolInt(*rulesEnabled))
	}
	if subsEnabled != nil {
		sets = append(sets, "subs_enabled = ?")
		args = append(args, boolInt(*subsEnabled))
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

// UpdateUserQuota 设置单个用户的配额覆盖。传 0 表示恢复按档位默认，
// 传负数表示不限。三个值各自独立。
func (s *Store) UpdateUserQuota(ctx context.Context, uid string, maxRules, maxSubs, dailyQueries int) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE users SET custom_max_rules = ?, custom_max_subs = ?, custom_daily_queries = ? WHERE id = ?`,
		maxRules, maxSubs, dailyQueries, uid)
	if err != nil {
		return fmt.Errorf("更新用户配额失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearUserLogs 把用户的日志视图起点推到 now。
//
// 刻意不 DELETE：用户手动清空只该清掉「自己看到的」，
// 管理员那边的审计记录必须留着 —— 否则用户一清，出了问题就查无实据。
// 真正的删除只由保留策略（Cleanup）按时间做。
func (s *Store) ClearUserLogs(ctx context.Context, uid string, now int64) error {
	res, err := s.write.ExecContext(ctx, `UPDATE users SET log_cleared_at = ? WHERE id = ?`, now, uid)
	if err != nil {
		return fmt.Errorf("清空用户日志失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetAdminPassword 按用户名重置管理员密码。
//
// 存在的意义是「找回」：管理员密码一旦忘记，面板进不去、也没有任何接口
// 能改回来，整套系统就锁死了。这是自建服务必须留的逃生门。
//
// 只允许 role = admin 的行被改：否则拿到这台机器 shell 的人可以顺手
// 把任意普通用户的密码也换掉，而普通用户本该只能由管理员改。
func (s *Store) ResetAdminPassword(ctx context.Context, username, hash string) (string, error) {
	var uid string
	err := s.read.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username = ? AND role = ?`, username, model.RoleAdmin).Scan(&uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("查询管理员失败: %w", err)
	}
	if _, err := s.write.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, uid); err != nil {
		return "", fmt.Errorf("重置管理员密码失败: %w", err)
	}
	// 密码换了，旧的刷新令牌必须全部失效，否则已经拿到令牌的人还能继续用。
	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = ?`, uid); err != nil {
		return "", fmt.Errorf("吊销旧令牌失败: %w", err)
	}
	return uid, nil
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

// ResetClientID 给用户换一个新的 client_id，并把重置次数 +1。
//
// 换 id 等于换掉接入地址：旧子域立刻失效，客户端需要重新配置。
// 次数上限由全局设置控制，这里只负责执行和计数。
func (s *Store) ResetClientID(ctx context.Context, uid string) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		cid := id.NewClientID()
		res, err := s.write.ExecContext(ctx,
			`UPDATE users SET client_id = ?, id_reset_count = id_reset_count + 1 WHERE id = ?`,
			cid, uid)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				return "", ErrNotFound
			}
			return cid, nil
		}
		// client_id 撞车就换一个重试；其余错误直接返回
		if isUniqueViolation(err, "users.client_id") {
			continue
		}
		return "", fmt.Errorf("重置 client_id 失败: %w", err)
	}
	return "", fmt.Errorf("重置 client_id 失败: 多次冲突")
}

// SetCustomClientID 把用户的 client_id 改成他自己指定的值，并标记这一次机会已用。
//
// 并发安全靠 SQL 里的 `custom_id_used = 0` 条件：两个请求同时提交时只有一个
// 能更新到行（RowsAffected 为 1），另一个拿到 ErrNotFound —— 在 Go 侧先查后写
// 挡不住这种竞争，而「只能一次」恰恰是最容易被连点两下绕过的地方。
func (s *Store) SetCustomClientID(ctx context.Context, uid, cid string) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE users SET client_id = ?, custom_id_used = 1 WHERE id = ? AND custom_id_used = 0`,
		cid, uid)
	if err != nil {
		if isUniqueViolation(err, "users.client_id") {
			return ErrClientIDTaken
		}
		return fmt.Errorf("设置 client_id 失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 要么用户不存在，要么机会已经用过了；由调用方再查一次区分。
		return ErrNotFound
	}
	return nil
}

// AdminSetClientID 由管理员改 client_id，不受「只能一次」限制，也不消耗用户的机会。
//
// 管理员是兜底手段：用户填错、或者机会用完之后还想换，只能走这里。
func (s *Store) AdminSetClientID(ctx context.Context, uid, cid string) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE users SET client_id = ? WHERE id = ?`, cid, uid)
	if err != nil {
		if isUniqueViolation(err, "users.client_id") {
			return ErrClientIDTaken
		}
		return fmt.Errorf("设置 client_id 失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClientIDTaken 判断某个 client_id 是否已被别人占用。
//
// excludeUID 用于「改自己的」场景：用户提交和当前相同的值时不该判成撞车。
func (s *Store) ClientIDTaken(ctx context.Context, cid, excludeUID string) (bool, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE client_id = ? AND id <> ?`, cid, excludeUID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("检查 client_id 是否占用失败: %w", err)
	}
	return n > 0, nil
}

// BulkDeleteUsers 批量删除用户，返回实际删除数量。
//
// 放在一个事务里：删到一半失败会把已删的也回滚，避免管理员看到「删了 3 个报了错」
// 却不知道到底删掉了几个。skipID 用于把操作者自己排除掉（不能把自己删了）。
func (s *Store) BulkDeleteUsers(ctx context.Context, ids []string, skipID string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	deleted := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, uid := range ids {
			if uid == "" || uid == skipID {
				continue
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid)
			if err != nil {
				return fmt.Errorf("删除用户 %s 失败: %w", uid, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				deleted++
			}
		}
		return nil
	})
	return deleted, err
}

// UserUsage 是单个用户的规则/订阅占用。
type UserUsage struct {
	UserID string `json:"user_id"`
	Rules  int    `json:"rules"`
	Subs   int    `json:"subs"`
}

// RuleSubUsage 一次性统计所有用户的规则与订阅数量。
//
// 后台用户列表要显示「3/50 条规则」，逐个用户查会是 N+1；
// 这里用两条 GROUP BY 拿到全量再拼，用户数上千也不会有压力。
func (s *Store) RuleSubUsage(ctx context.Context) ([]UserUsage, error) {
	usage := map[string]*UserUsage{}
	get := func(uid string) *UserUsage {
		if u, ok := usage[uid]; ok {
			return u
		}
		u := &UserUsage{UserID: uid}
		usage[uid] = u
		return u
	}

	rows, err := s.read.QueryContext(ctx,
		`SELECT user_id, COUNT(*) FROM rules WHERE is_global = 0 GROUP BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("统计规则占用失败: %w", err)
	}
	for rows.Next() {
		var uid string
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(uid).Rules = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.read.QueryContext(ctx,
		`SELECT user_id, COUNT(*) FROM subscriptions WHERE is_global = 0 GROUP BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("统计订阅占用失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			return nil, err
		}
		get(uid).Subs = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]UserUsage, 0, len(usage))
	for _, u := range usage {
		out = append(out, *u)
	}
	return out, nil
}
