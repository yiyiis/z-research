// Package store — evaluation.go 实现报告评估分数的持久化。
//
// 仿照 multiagent/checkpoint.go 的模式:复用项目的 SQLite DB,通过类型断言
// 拿到底层 *sql.DB,用 ensureTable 幂等建表。不修改 reports 表结构(避免 migration)。
//
// 一份报告可有多次评估(支持重评),取最新一次展示。综合分冗余存一列,
// 便于历史列表按分数排序/过滤而无需解析 JSON。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Evaluation 是一次评估的持久化记录。
type Evaluation struct {
	ID        int64     `json:"id"`
	ReportID  int64     `json:"report_id"`
	ScoreJSON string    `json:"score_json"`   // 完整 Score 结构的 JSON(由 eval 包序列化)
	Overall   float64   `json:"overall"`      // 冗余综合分,便于排序
	CreatedAt time.Time `json:"created_at"`
}

// SaveEvaluationInput 是 SaveEvaluation 的入参(用 struct 避免长参数列表)。
type SaveEvaluationInput struct {
	ReportID  int64
	ScoreJSON string  // 完整 Score 结构的 JSON 字符串
	Overall   float64 // 综合分(冗余,便于排序)
}

// SQLiteEvaluationStore 是基于 SQLite 的评估持久化实现。
type SQLiteEvaluationStore struct {
	st Store
	mu sync.Mutex
	db *sql.DB
}

// NewSQLiteEvaluationStore 创建评估 store,首次调用时建表。
func NewSQLiteEvaluationStore(ctx context.Context, st Store) (*SQLiteEvaluationStore, error) {
	if st == nil {
		return nil, errors.New("nil store")
	}
	es := &SQLiteEvaluationStore{st: st}
	if err := es.ensureTable(ctx); err != nil {
		return nil, err
	}
	return es, nil
}

// SaveEvaluation 保存一次评估。返回带 ID 的 Evaluation。
func (e *SQLiteEvaluationStore) SaveEvaluation(ctx context.Context, in SaveEvaluationInput) (*Evaluation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	db, err := e.openDB(ctx)
	if err != nil {
		return nil, err
	}
	// 校验 score_json 是合法 JSON(防止存入垃圾)。
	if !json.Valid([]byte(in.ScoreJSON)) {
		return nil, fmt.Errorf("SaveEvaluation: score_json 不是合法 JSON")
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO report_evaluations (report_id, score_json, overall, created_at)
		VALUES (?, ?, ?, strftime('%s','now'))
	`, in.ReportID, in.ScoreJSON, in.Overall)
	if err != nil {
		return nil, fmt.Errorf("SaveEvaluation: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Evaluation{
		ID:        id,
		ReportID:  in.ReportID,
		ScoreJSON: in.ScoreJSON,
		Overall:   in.Overall,
		CreatedAt: time.Now(),
	}, nil
}

// GetLatestEvaluation 取一篇报告的最新一次评估。
// 用 ORDER BY id DESC 而非 created_at DESC——id 自增一定后插入的更大,
// 避免秒级精度下同秒多次评估时排序不稳定。
// 没有评估记录时返回 (nil, nil)(不报错,调用方据此决定是否展示评分)。
func (e *SQLiteEvaluationStore) GetLatestEvaluation(ctx context.Context, reportID int64) (*Evaluation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	db, err := e.openDB(ctx)
	if err != nil {
		return nil, err
	}
	var ev Evaluation
	var createdAt int64
	row := db.QueryRowContext(ctx, `
		SELECT id, report_id, score_json, overall, created_at
		FROM report_evaluations
		WHERE report_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, reportID)
	if err := row.Scan(&ev.ID, &ev.ReportID, &ev.ScoreJSON, &ev.Overall, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // 没有评估记录,正常情况
		}
		return nil, fmt.Errorf("GetLatestEvaluation: %w", err)
	}
	ev.CreatedAt = time.Unix(createdAt, 0)
	return &ev, nil
}

// GetEvaluationsByReportIDs 批量取多篇报告的最新评估(用于历史列表展示分数)。
// 返回 map[reportID]*Evaluation,没有评估的报告不在 map 里。
func (e *SQLiteEvaluationStore) GetEvaluationsByReportIDs(ctx context.Context, reportIDs []int64) (map[int64]*Evaluation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(reportIDs) == 0 {
		return nil, nil
	}
	db, err := e.openDB(ctx)
	if err != nil {
		return nil, err
	}
	// 用子查询取每篇报告的最新评估(id 最大的那条,等价于时间最新但无秒级精度问题)。
	rows, err := db.QueryContext(ctx, `
		SELECT id, report_id, score_json, overall, created_at
		FROM (
			SELECT id, report_id, score_json, overall, created_at,
			ROW_NUMBER() OVER (PARTITION BY report_id ORDER BY id DESC) AS rn
			FROM report_evaluations
			WHERE report_id IN (`+placeholders(len(reportIDs))+`)
		) WHERE rn = 1
	`, toAnyArgs(reportIDs)...)
	if err != nil {
		return nil, fmt.Errorf("GetEvaluationsByReportIDs: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]*Evaluation, len(reportIDs))
	for rows.Next() {
		var ev Evaluation
		var createdAt int64
		if err := rows.Scan(&ev.ID, &ev.ReportID, &ev.ScoreJSON, &ev.Overall, &createdAt); err != nil {
			return nil, fmt.Errorf("GetEvaluationsByReportIDs scan: %w", err)
		}
		ev.CreatedAt = time.Unix(createdAt, 0)
		out[ev.ReportID] = &ev
	}
	return out, nil
}

// ensureTable 幂等建表。
func (e *SQLiteEvaluationStore) ensureTable(ctx context.Context) error {
	db, err := e.openDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS report_evaluations (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			report_id  INTEGER NOT NULL,
			score_json TEXT    NOT NULL,
			overall    REAL    NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			FOREIGN KEY (report_id) REFERENCES reports(id)
		);
		CREATE INDEX IF NOT EXISTS idx_eval_report ON report_evaluations (report_id);
		CREATE INDEX IF NOT EXISTS idx_eval_created ON report_evaluations (created_at DESC);
	`)
	if err != nil {
		return fmt.Errorf("create report_evaluations: %w", err)
	}
	return nil
}

// openDB 通过类型断言拿到底层 *sql.DB(只支持 SQLiteStore)。
func (e *SQLiteEvaluationStore) openDB(ctx context.Context) (*sql.DB, error) {
	type sqlDBProvider interface {
		DB() *sql.DB
	}
	if p, ok := e.st.(sqlDBProvider); ok {
		return p.DB(), nil
	}
	return nil, fmt.Errorf("evaluation store: underlying store does not expose *sql.DB (got %T)", e.st)
}

// placeholders 生成 N 个 "?"(用于 IN 查询)。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "?"
	}
	return out
}

// toAnyArgs 把 []int64 转成 []any(用于 sql 查询参数)。
func toAnyArgs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
