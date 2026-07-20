package store

import (
	"context"
	"encoding/json"
	"testing"
)

func newTestEvalStore(t *testing.T) *SQLiteEvaluationStore {
	t.Helper()
	s := newTestStore(t)
	es, err := NewSQLiteEvaluationStore(context.Background(), s)
	if err != nil {
		t.Fatalf("NewSQLiteEvaluationStore: %v", err)
	}
	return es
}

// saveTestReport 存一个最小报告,返回它的 ID(评估需要关联 report_id)。
func saveTestReport(t *testing.T, s *SQLiteStore, query string) int64 {
	t.Helper()
	r, err := s.Save(context.Background(), query, "title", "content", nil)
	if err != nil {
		t.Fatalf("Save report: %v", err)
	}
	return r.ID
}

func TestEvalStore_SaveAndGet(t *testing.T) {
	es := newTestEvalStore(t)
	reportID := saveTestReport(t, newTestStore(t), "test query")

	scoreJSON := `{"overall":8.5,"dimensions":{"coverage":{"score":9}}}`
	ev, err := es.SaveEvaluation(context.Background(), SaveEvaluationInput{
		ReportID:  reportID,
		ScoreJSON: scoreJSON,
		Overall:   8.5,
	})
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	if ev.ID == 0 || ev.ReportID != reportID {
		t.Errorf("returned Evaluation wrong: %+v", ev)
	}

	// 读回最新评估。
	got, err := es.GetLatestEvaluation(context.Background(), reportID)
	if err != nil {
		t.Fatalf("GetLatestEvaluation: %v", err)
	}
	if got == nil {
		t.Fatal("GetLatestEvaluation returned nil")
	}
	if got.ScoreJSON != scoreJSON || got.Overall != 8.5 {
		t.Errorf("got = %+v, want score_json=%s overall=8.5", got, scoreJSON)
	}
}

func TestEvalStore_GetLatest_None(t *testing.T) {
	es := newTestEvalStore(t)
	reportID := saveTestReport(t, newTestStore(t), "q")

	got, err := es.GetLatestEvaluation(context.Background(), reportID)
	if err != nil {
		t.Fatalf("GetLatestEvaluation: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unevaluated report, got %+v", got)
	}
}

func TestEvalStore_GetLatest_PicksNewest(t *testing.T) {
	es := newTestEvalStore(t)
	reportID := saveTestReport(t, newTestStore(t), "q")

	// 存两次评估(模拟重评)。
	es.SaveEvaluation(context.Background(), SaveEvaluationInput{
		ReportID: reportID, ScoreJSON: `{"v":1}`, Overall: 5.0,
	})
	es.SaveEvaluation(context.Background(), SaveEvaluationInput{
		ReportID: reportID, ScoreJSON: `{"v":2}`, Overall: 8.0,
	})

	got, err := es.GetLatestEvaluation(context.Background(), reportID)
	if err != nil {
		t.Fatalf("GetLatestEvaluation: %v", err)
	}
	if got.Overall != 8.0 {
		t.Errorf("expected newest (8.0), got overall=%.1f", got.Overall)
	}
}

func TestEvalStore_BatchGet(t *testing.T) {
	es := newTestEvalStore(t)
	s := newTestStore(t)
	id1 := saveTestReport(t, s, "q1")
	id2 := saveTestReport(t, s, "q2")
	id3 := saveTestReport(t, s, "q3") // 这篇不评估

	es.SaveEvaluation(context.Background(), SaveEvaluationInput{ReportID: id1, ScoreJSON: `{}`, Overall: 7.0})
	es.SaveEvaluation(context.Background(), SaveEvaluationInput{ReportID: id2, ScoreJSON: `{}`, Overall: 9.0})

	m, err := es.GetEvaluationsByReportIDs(context.Background(), []int64{id1, id2, id3})
	if err != nil {
		t.Fatalf("GetEvaluationsByReportIDs: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("map size = %d, want 2 (id3 没评估不应在 map)", len(m))
	}
	if m[id1].Overall != 7.0 || m[id2].Overall != 9.0 {
		t.Errorf("batch results wrong: %+v", m)
	}
	if _, ok := m[id3]; ok {
		t.Error("id3 不应在 map 里(未评估)")
	}
}

func TestEvalStore_BatchGet_Empty(t *testing.T) {
	es := newTestEvalStore(t)
	m, err := es.GetEvaluationsByReportIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if m != nil {
		t.Errorf("empty batch should return nil, got %+v", m)
	}
}

func TestEvalStore_RejectsInvalidJSON(t *testing.T) {
	es := newTestEvalStore(t)
	reportID := saveTestReport(t, newTestStore(t), "q")

	_, err := es.SaveEvaluation(context.Background(), SaveEvaluationInput{
		ReportID:  reportID,
		ScoreJSON: "not a json {{{",
		Overall:   5,
	})
	if err == nil {
		t.Error("应拒绝非法 JSON")
	}
}

// 编译期断言:确保 json 包被使用(避免 import 警告,实际由 json.Valid 调用)。
var _ = json.Valid
