package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

var (
	ErrBulkMemberDays     = errors.New("days 必须在 1 到 36500 之间")
	ErrBulkMemberTier     = errors.New("tier_id 必须是存在的非默认会员档位")
	ErrBulkMemberKey      = errors.New("Idempotency-Key 必须为 1 到 128 个可见 ASCII 字符，不允许逗号")
	ErrBulkMemberConflict = errors.New("Idempotency-Key 已用于其他批量发放参数")
)

type BulkMemberRequest struct {
	TierID         string
	Days           int
	DryRun         bool
	IdempotencyKey string
	ActorID        string
	IP             string
}

type BulkMemberResult struct {
	Users    int    `json:"users"`
	Tier     string `json:"tier"`
	Days     int    `json:"days"`
	DryRun   bool   `json:"dry_run"`
	Replayed bool   `json:"replayed"`
}

// BulkGrantMembership applies a grant to every role=user account, or previews
// the number of changed rows. The endpoint's keys are global across admins.
func (s *Store) BulkGrantMembership(ctx context.Context, req BulkMemberRequest) (BulkMemberResult, error) {
	var result BulkMemberResult
	req.TierID = strings.TrimSpace(req.TierID)
	if req.DryRun {
		tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return result, err
		}
		defer tx.Rollback()
		return runBulkMembership(ctx, tx, req)
	}

	key := strings.TrimSpace(req.IdempotencyKey)
	if len(key) < 1 || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool {
		return c < '!' || c > '~' || c == ','
	}) {
		return result, ErrBulkMemberKey
	}
	// Reuse the existing meta table. A namespaced digest keeps raw request
	// keys out of the database, and the ledger has no expiry.
	ledgerKey := fmt.Sprintf("bulk_member.v1.%x", sha256.Sum256([]byte(key)))
	// The write pool starts BEGIN IMMEDIATE, so lookup, grant and record
	// insertion are serialized even across separate Store connections.
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, ledgerKey).Scan(&saved)
	if err == nil {
		if err := json.Unmarshal([]byte(saved), &result); err != nil {
			return BulkMemberResult{}, fmt.Errorf("读取批量发放记录失败: %w", err)
		}
		if result.Days != req.Days || result.Tier != req.TierID {
			return BulkMemberResult{}, ErrBulkMemberConflict
		}
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}

	result, err = runBulkMembership(ctx, tx, req)
	if err != nil {
		return BulkMemberResult{}, err
	}
	record, err := json.Marshal(result)
	if err != nil {
		return BulkMemberResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES (?, ?)`, ledgerKey, string(record)); err != nil {
		return BulkMemberResult{}, fmt.Errorf("保存批量发放记录失败: %w", err)
	}
	detail := fmt.Sprintf("tier=%s days=%d users=%d", result.Tier, result.Days, result.Users)
	if err := insertAudit(ctx, tx, req.ActorID, "admin_bulk_grant_membership", detail, req.IP); err != nil {
		return BulkMemberResult{}, fmt.Errorf("保存批量发放审计失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BulkMemberResult{}, fmt.Errorf("提交批量发放事务失败: %w", err)
	}
	return result, nil
}

func runBulkMembership(ctx context.Context, tx *sql.Tx, req BulkMemberRequest) (BulkMemberResult, error) {
	result := BulkMemberResult{Tier: req.TierID, Days: req.Days, DryRun: req.DryRun}
	if req.Days < 1 || req.Days > 36500 {
		return result, ErrBulkMemberDays
	}
	tiers, err := tierMap(ctx, tx)
	if err != nil {
		return result, err
	}
	if tier, ok := tiers[req.TierID]; !ok || tier.IsDefault || req.TierID == string(model.TierFree) {
		return result, ErrBulkMemberTier
	}
	changes, err := planBulkMembership(ctx, tx, req, tiers)
	result.Users = len(changes)
	if err != nil || req.DryRun || len(changes) == 0 {
		return result, err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE users SET member_until = ?, tier_id = ? WHERE id = ? AND role = ?`)
	if err != nil {
		return result, err
	}
	defer stmt.Close()
	for _, change := range changes {
		if _, err := stmt.ExecContext(ctx, change.until, change.tier, change.id, model.RoleUser); err != nil {
			return result, fmt.Errorf("批量更新会员失败: %w", err)
		}
	}
	return result, nil
}

type bulkMemberChange struct {
	id, tier string
	until    int64
}

func planBulkMembership(ctx context.Context, tx *sql.Tx, req BulkMemberRequest, tiers map[string]model.TierDef) ([]bulkMemberChange, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, member_until, tier_id FROM users WHERE role = ?`, model.RoleUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().Unix()
	var changes []bulkMemberChange
	for rows.Next() {
		var id, tier string
		var until int64
		if err := rows.Scan(&id, &until, &tier); err != nil {
			return nil, err
		}
		currentTier := tier
		if until > now {
			// Empty or stale tier ids already have an effective member tier.
			currentTier = model.GrantableTierID(tiers, tier)
		}
		// Clamp before adding to handle historical values above the sentinel
		// safely, and cap the result to the supported permanent limit.
		next, applied := membershipGrant(min(until, model.MemberForever), currentTier, req.TierID, req.Days, now, tiers)
		next = min(next, model.MemberForever)
		if next != until || applied != tier {
			changes = append(changes, bulkMemberChange{id, applied, next})
		}
	}
	return changes, rows.Err()
}
