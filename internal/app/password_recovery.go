package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrRecoveryAccount = errors.New("请选择仍处于启用状态的现有超管账号；不会创建账号或恢复已停用账号")
var ErrRecoveryPassword = errors.New("新" + PasswordRequirement)

// ResetAdminPassword is for the elevated local maintenance wizard only.
// It must never be exposed as an unauthenticated site endpoint.
func ResetAdminPassword(ctx context.Context, db *sql.DB, username, password string) error {
	if !ValidPassword(password) {
		return ErrRecoveryPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, role, account, mobile string
	var enabled bool
	err = tx.QueryRowContext(ctx, "SELECT id,role,enabled,username,COALESCE(mobile,'') FROM users WHERE username=? FOR UPDATE", username).Scan(&id, &role, &enabled, &account, &mobile)
	if err == sql.ErrNoRows {
		return ErrRecoveryAccount
	}
	if err != nil {
		return err
	}
	if role != "admin" || !enabled {
		return ErrRecoveryAccount
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET password_hash=? WHERE id=?", string(hash), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return err
	}
	// Clear only this account's password failures, retaining SMS and other users' limits.
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_attempts WHERE purpose='password' AND subject_hash IN (?,?)", digest(strings.ToLower(account)), digest(mobile)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_logs(user_id,action,target_id,result) VALUES('','local_admin_password_reset',?,'ok')", id); err != nil {
		return err
	}
	return tx.Commit()
}
