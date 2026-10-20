package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func OpenDB(c Config) (*sql.DB, error) {
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, fmt.Errorf("数据库连接失败，请检查配置及服务状态: %w", e)
	}
	return db, nil
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS site_settings (id TINYINT PRIMARY KEY,settings LONGTEXT NOT NULL,logo MEDIUMBLOB NULL,revision BIGINT NOT NULL DEFAULT 1) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sms_settings (id TINYINT PRIMARY KEY,settings LONGTEXT NOT NULL,revision BIGINT NOT NULL DEFAULT 1) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS departments (id CHAR(32) PRIMARY KEY,name VARCHAR(100) NOT NULL,parent_id CHAR(32) NOT NULL DEFAULT '',created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS users (id CHAR(32) PRIMARY KEY,username VARCHAR(32) NOT NULL UNIQUE,display_name VARCHAR(100) NOT NULL,mobile VARCHAR(11) NULL UNIQUE,password_hash VARCHAR(255) NOT NULL,role VARCHAR(20) NOT NULL,dept_id CHAR(32) NOT NULL DEFAULT '',enabled BOOLEAN NOT NULL DEFAULT TRUE,quota_bytes BIGINT NOT NULL,created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sessions (token_hash CHAR(64) PRIMARY KEY,user_id CHAR(32) NOT NULL,csrf CHAR(64) NOT NULL,expires_at DATETIME(6) NOT NULL,INDEX(user_id),INDEX(expires_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS entries (id CHAR(32) PRIMARY KEY,parent_id CHAR(32) NOT NULL DEFAULT '',space_key VARCHAR(64) NOT NULL,scope VARCHAR(16) NOT NULL,owner_id CHAR(32) NOT NULL,dept_id CHAR(32) NOT NULL DEFAULT '',kind VARCHAR(10) NOT NULL,name VARCHAR(255) NOT NULL,name_hash CHAR(64) NOT NULL,remark TEXT NOT NULL,size BIGINT NOT NULL DEFAULT 0,mime VARCHAR(150) NOT NULL DEFAULT 'application/octet-stream',storage_kind VARCHAR(16) NOT NULL DEFAULT '',storage_path TEXT NULL,source_key CHAR(64) NULL UNIQUE,source_root_hash CHAR(64) NOT NULL DEFAULT '',import_seen CHAR(32) NOT NULL DEFAULT '',sha256 CHAR(64) NOT NULL DEFAULT '',scan_status VARCHAR(20) NOT NULL DEFAULT 'pending',scan_note VARCHAR(255) NOT NULL DEFAULT '',publication VARCHAR(16) NOT NULL DEFAULT 'published',deleted BOOLEAN NOT NULL DEFAULT FALSE,trash_batch CHAR(32) NOT NULL DEFAULT '',source_mtime BIGINT NOT NULL DEFAULT 0,source_missing BOOLEAN NOT NULL DEFAULT FALSE,created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),UNIQUE KEY entry_name(space_key,parent_id,name_hash),INDEX entries_list(space_key,parent_id,deleted,publication),INDEX scan_queue(scan_status,kind,deleted)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS uploads (id CHAR(32) PRIMARY KEY,user_id CHAR(32) NOT NULL,space_key VARCHAR(64) NOT NULL,scope VARCHAR(16) NOT NULL,dept_id CHAR(32) NOT NULL DEFAULT '',parent_id CHAR(32) NOT NULL DEFAULT '',name VARCHAR(255) NOT NULL,remark TEXT NOT NULL,size BIGINT NOT NULL,total_chunks INT NOT NULL,status VARCHAR(16) NOT NULL DEFAULT 'uploading',entry_id CHAR(32) NOT NULL DEFAULT '',expires_at DATETIME(6) NOT NULL,created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),INDEX(user_id,status,expires_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS upload_chunks (upload_id CHAR(32) NOT NULL,chunk_index INT NOT NULL,size BIGINT NOT NULL,sha256 CHAR(64) NOT NULL,PRIMARY KEY(upload_id,chunk_index)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS import_jobs (id CHAR(32) PRIMARY KEY,status VARCHAR(20) NOT NULL DEFAULT 'running',root_hash CHAR(64) NOT NULL,user_id CHAR(32) NOT NULL,added BIGINT NOT NULL DEFAULT 0,updated BIGINT NOT NULL DEFAULT 0,skipped BIGINT NOT NULL DEFAULT 0,errors BIGINT NOT NULL DEFAULT 0,current_path VARCHAR(1024) NOT NULL DEFAULT '',error_note VARCHAR(255) NOT NULL DEFAULT '',created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),finished_at DATETIME(6) NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS audit_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY,user_id CHAR(32) NOT NULL DEFAULT '',action VARCHAR(50) NOT NULL,target_id VARCHAR(64) NOT NULL DEFAULT '',result VARCHAR(30) NOT NULL DEFAULT 'ok',created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),INDEX(created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS auth_attempts (id BIGINT AUTO_INCREMENT PRIMARY KEY,subject_hash CHAR(64) NOT NULL,ip_hash CHAR(64) NOT NULL,purpose VARCHAR(20) NOT NULL,created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),INDEX(subject_hash,purpose,created_at),INDEX(ip_hash,purpose,created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sms_cooldowns (mobile VARCHAR(11) PRIMARY KEY,sent_at DATETIME(6) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

func Migrate(db *sql.DB) error {
	for i, s := range schema {
		if _, e := db.Exec(s); e != nil {
			return fmt.Errorf("建表步骤 %d 失败: %w", i+1, e)
		}
	}
	return nil
}
