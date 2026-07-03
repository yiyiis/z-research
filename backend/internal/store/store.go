// Package store 提供研究报告的持久化存储，当前实现为 SQLite（纯 Go 驱动 modernc.org/sqlite，无 CGO）。
//
// Store 接口与具体实现分离，便于后续替换为其他后端（如 Postgres）或测试时注入内存实现。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，注册 driver name "sqlite"
)

// Report 是一条持久化的研究报告。
type Report struct {
	ID        int64     `json:"id"`
	Query     string    `json:"query"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"` // 列表查询时不返回正文，省流量
	Sources   []Source  `json:"sources"`
	CreatedAt time.Time `json:"created_at"`
}

// Source 是报告引用的一条来源（与 researcher.Source 结构对齐，但 store 包不依赖 researcher）。
type Source struct {
	N     int    `json:"n"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Store 定义报告持久化接口。
type Store interface {
	// Save 保存一篇报告，返回带 ID 的 Report。
	Save(ctx context.Context, query, title, content string, sources []Source) (*Report, error)
	// Get 按 ID 取单篇报告（含正文）。
	Get(ctx context.Context, id int64) (*Report, error)
	// List 列出最近 limit 篇报告（不含正文，按时间倒序）。limit<=0 时用默认值。
	List(ctx context.Context, limit int) ([]*Report, error)
	// Delete 按 ID 删除一篇报告。
	Delete(ctx context.Context, id int64) error
	// Close 关闭底层连接。
	Close() error
}

// SQLiteStore 是基于 modernc.org/sqlite 的 Store 实现。
type SQLiteStore struct {
	db *sql.DB
}

// New 打开（或创建）一个 SQLite 数据库文件并初始化表结构。
func New(ctx context.Context, dbPath string) (*SQLiteStore, error) {
	// _pragma 设置：WAL 模式提升并发读；busy_timeout 避免写锁冲突时立即失败。
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}
	// SQLite 写入串行，单连接即可避免锁竞争。
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// init 执行建表语句（schema.sql 内联）。
func (s *SQLiteStore) init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("初始化表结构失败: %w", err)
	}
	return nil
}

// Save 插入一篇报告。
func (s *SQLiteStore) Save(ctx context.Context, query, title, content string, sources []Source) (*Report, error) {
	srcJSON, err := json.Marshal(sources)
	if err != nil {
		return nil, fmt.Errorf("序列化 sources 失败: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO reports (query, title, content, sources) VALUES (?, ?, ?, ?)`,
		query, title, content, string(srcJSON))
	if err != nil {
		return nil, fmt.Errorf("插入报告失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("获取自增 ID 失败: %w", err)
	}
	// 回读 created_at，保证返回值与库中一致。
	r, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Get 按 ID 取单篇报告（含正文）。
func (s *SQLiteStore) Get(ctx context.Context, id int64) (*Report, error) {
	var (
		r        Report
		srcJSON  string
		createdS int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, query, title, content, sources, created_at FROM reports WHERE id = ?`, id).
		Scan(&r.ID, &r.Query, &r.Title, &r.Content, &srcJSON, &createdS)
	if err != nil {
		return nil, fmt.Errorf("查询报告 %d 失败: %w", id, err)
	}
	if err := json.Unmarshal([]byte(srcJSON), &r.Sources); err != nil {
		// 旧数据或损坏，容错为空列表。
		r.Sources = nil
	}
	r.CreatedAt = time.Unix(createdS, 0)
	return &r, nil
}

// List 列出最近 limit 篇报告（不含正文，按时间倒序）。
func (s *SQLiteStore) List(ctx context.Context, limit int) ([]*Report, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, query, title, sources, created_at FROM reports ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询报告列表失败: %w", err)
	}
	defer rows.Close()

	var reports []*Report
	for rows.Next() {
		var (
			r        Report
			srcJSON  string
			createdS int64
		)
		if err := rows.Scan(&r.ID, &r.Query, &r.Title, &srcJSON, &createdS); err != nil {
			return nil, fmt.Errorf("扫描报告行失败: %w", err)
		}
		_ = json.Unmarshal([]byte(srcJSON), &r.Sources) // 容错
		r.CreatedAt = time.Unix(createdS, 0)
		reports = append(reports, &r)
	}
	return reports, rows.Err()
}

// Delete 按 ID 删除一篇报告。
func (s *SQLiteStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除报告 %d 失败: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("报告 %d 不存在", id)
	}
	return nil
}

// Close 关闭底层连接。
func (s *SQLiteStore) Close() error { return s.db.Close() }

// schemaSQL 是建表语句（与 schema.sql 一致，内联以保证 New 即可自建）。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS reports (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    query      TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    content    TEXT    NOT NULL,
    sources    TEXT    NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_reports_created_at ON reports (created_at DESC);
`
