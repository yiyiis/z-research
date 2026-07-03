package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

//go:embed all:web
var webFS embed.FS

// Router 配置并返回一个 gin Engine，注册 WebSocket 研究端点、报告 CRUD 路由与 SPA 托管。
//
// dev 为 true 时启用宽松 CORS（开发期前端跑在 :5173 跨域访问 :8080）。
func (s *Server) Router(dev bool) *gin.Engine {
	if dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	if dev {
		// 开发期允许 Vite dev server (5173) 跨域调用 REST 接口。
		// WebSocket 的跨域由 wsUpgrader.CheckOrigin 处理，不依赖此处。
		r.Use(cors.New(cors.Config{
			AllowOrigins:     []string{"http://localhost:5173", "http://127.0.0.1:5173"},
			AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"*"},
			AllowCredentials: true,
		}))
	}

	// WebSocket 研究端点（全双工长连接，实时推送进度）。
	r.GET("/ws", s.handleResearch)

	// 报告 CRUD（普通 REST）。
	api := r.Group("/api")
	{
		api.GET("/reports", s.handleListReports)
		api.GET("/reports/:id", s.handleGetReport)
		api.DELETE("/reports/:id", s.handleDeleteReport)
	}

	// 托管内嵌的前端 SPA（生产）。
	staticFS, err := fs.Sub(webFS, "web")
	if err == nil && hasIndexHTML(staticFS) {
		registerSPA(r, staticFS)
	}

	return r
}

// hasIndexHTML 检查内嵌 FS 是否包含真实前端产物（而非仅 .gitkeep）。
func hasIndexHTML(fsys fs.FS) bool {
	f, err := fsys.Open("index.html")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// registerSPA 把前端 SPA 注册到路由：静态资源直出，其余路径回退到 index.html。
func registerSPA(r *gin.Engine, fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))

	// 所有非 /api、非 /ws、非静态文件的路径 → 返回 index.html（支持前端路由刷新不 404）。
	r.NoRoute(func(c *gin.Context) {
		path := strings.TrimPrefix(c.Request.URL.Path, "/")

		// 若请求的是 /api/* 或 /ws 未匹配，返回 404 JSON 而非 HTML。
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/ws") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		// 若静态资源存在，直接返回（如 /assets/xxx.js）。
		if path != "" {
			if f, err := fsys.Open(path); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		// 否则回退到 index.html（SPA 客户端路由）。
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
