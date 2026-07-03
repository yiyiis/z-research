package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"

	"github.com/gin-gonic/gin"
)

// doReq 发起一个 JSON 请求并返回响应（用于 REST CRUD 测试）。
func doReq(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
