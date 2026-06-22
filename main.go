package main

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"os"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"shehua-tg/core"
)

// 全局模板变量（缓存，提升性能）
var (
	loginTemplate *template.Template
	indexTemplate *template.Template
)

func init() {
	var err error
	loginTemplate, err = template.ParseFiles("templates/login.html")
	if err != nil {
		log.Printf("警告: 登录模板解析失败: %v", err)
		loginTemplate = template.New("login")
	}
	indexTemplate, err = template.ParseFiles("templates/index.html")
	if err != nil {
		log.Printf("警告: 主页面模板解析失败: %v", err)
		indexTemplate = template.New("index")
	}
}

func main() {
	if err := core.LoadConfig(); err != nil {
		log.Printf("加载配置失败，使用默认: %v", err)
		_ = core.SaveConfig()
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Static("/assets", "./assets")

	// ---------- 会话中间件 ----------
	secret := []byte("cayflow-secret-key-2024")
	store := cookie.NewStore(secret)
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
	r.Use(sessions.Sessions("cayflow_session", store))

	// ---------- 公开路由 ----------
	r.GET("/login", serveLogin)
	r.POST("/api/login", loginHandler)
	r.POST("/api/logout", logoutHandler)

	// ---------- 需要认证的路由 ----------
	auth := r.Group("/")
	auth.Use(AuthMiddleware())
	{
		auth.GET("/", serveIndex)
		auth.GET("/api/config", getConfigHandler)
		auth.POST("/api/config", updateConfigHandler)
		auth.POST("/api/sehua/fetch", sehuaFetchHandler)
		auth.POST("/api/notification/tg/create", createChannelHandler)
		auth.POST("/api/notification/tg/update", updateChannelHandler)
		auth.POST("/api/notification/tg/delete", deleteChannelHandler)
		auth.POST("/api/notification/tg/test", testChannelHandler) 
		api := auth.Group("/api")
		core.Register115Routes(api)
		auth.GET("/api/sehua/list", sehuaListHandler)
		auth.GET("/api/sehua/image/:filename", sehuaImageHandler)
		auth.POST("/api/sehua/download", sehuaDownloadHandler)
		auth.POST("/api/sehua/crawl", sehuaCrawlHandler)
		auth.GET("/api/sehua/status", sehuaStatusHandler)
	}
	go func() {
		if err := core.StartTelegramBot(); err != nil {
			log.Printf("⚠️ TG 机器人启动失败: %v", err)
		}
	}()
	go func() {
		sections := []int{2, 36, 103, 151}
		dbPath := "/app/data/data.db"
		yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
		for _, section := range sections {
			hasData := false
			db, err := sql.Open("sqlite3", dbPath)
			if err == nil {
				var count int
				err = db.QueryRow(`SELECT COUNT(*) FROM sehua_data WHERE publish_date = ? AND section_id = ?`, yesterday, section).Scan(&count)
				if err == nil && count > 0 {
					hasData = true
					log.Printf("✅ 数据库已存在昨日 (%s) 数据 (板块 %d)，跳过首次爬取", yesterday, section)
				}
				db.Close()
			}
			if !hasData {
				for {
					log.Printf("🚀 尝试启动爬虫（按日期获取昨日数据: %s，板块 %d）...", yesterday, section)
					err := core.RunSehuaSpider(yesterday, section, false)
					if err == nil {
						log.Printf("✅ 成功启动爬虫 (板块 %d)，等待其执行完成...", section)
						time.Sleep(5 * time.Second)
						break
					}
					if strings.Contains(err.Error(), "已在运行中") {
						log.Printf("⏳ 板块 %d 发现爬虫任务正在运行中，等待 10 秒后重试...", section)
						time.Sleep(10 * time.Second)
						continue
					}
					log.Printf("⚠️ 板块 %d 爬虫发生其他启动错误: %v，停止重试。", section, err)
					break
				}
			}
		}
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			duration := next.Sub(now)
			log.Printf("⏰ 下次爬虫调度时间: %v (约 %.1f 小时后)", next, duration.Hours())
			time.Sleep(duration)
yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
log.Println("🔄 执行定时爬虫（清理数据库和全量图片库）...")
imageDir := "/app/data/sehua"
log.Printf("正在清空图片目录: %s", imageDir)
if err := os.RemoveAll(imageDir); err != nil {
	log.Printf("⚠️ 删除图片目录失败: %v", err)
} else {
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		log.Printf("⚠️ 重建图片目录失败: %v", err)
	} else {
		log.Println("✅ 全量图片库清理完成，目录已重置为空。")
	}
}
for _, section := range sections {
	db, err := sql.Open("sqlite3", dbPath)
	if err == nil {
		_, _ = db.Exec(`DELETE FROM sehua_data WHERE publish_date = ? AND section_id = ?`, yesterday, section)
		db.Close()
		log.Printf("已清理昨日 (%s) 数据 (板块 %d)", yesterday, section)
	}
	for {
		log.Printf("🔄 尝试启动定时爬虫（板块 %d）...", section)
		err := core.RunSehuaSpider(yesterday, section, false)
					if err == nil {
						log.Printf("✅ 定时爬虫启动成功 (板块 %d)", section)
						time.Sleep(5 * time.Second)
						break
					}
					if strings.Contains(err.Error(), "已在运行中") {
						log.Printf("⏳ 定时任务发现爬虫正在运行中 (板块 %d)，等待 10 秒后重试...", section)
						time.Sleep(10 * time.Second)
						continue
					}
					log.Printf("❌ 定时爬虫发生其他错误 (板块 %d): %v", section, err)
					break
				}
			}
		}
	}()

	log.Println("🚀 服务启动在 http://0.0.0.0:9898")
	if err := r.Run(":9898"); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// ==================== 登录与认证 ====================

func serveLogin(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Version": "v1.0.5",
	}
	if err := loginTemplate.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "渲染登录页面失败: %v", err)
	}
}

func serveIndex(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	cfg := core.GetConfig()
	data := gin.H{
		"Version":             "v1.0.5",
		"Username":            cfg.System.Username,
		"NotificationEnabled": cfg.Notification.Enabled,
	}
	if err := indexTemplate.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "渲染主页面失败: %v", err)
	}
}

func loginHandler(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "参数错误"})
		return
	}
	cfg := core.GetConfig()
	expectedUser := cfg.System.Username
	expectedPass := cfg.System.Password
	if expectedUser == "" {
		expectedUser = "admin"
		expectedPass = "admin"
	}
	if req.Username == expectedUser && req.Password == expectedPass {
		session := sessions.Default(c)
		session.Set("authenticated", true)
		session.Save()
		c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "登录成功"})
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "msg": "用户名或密码错误"})
	}
}

func logoutHandler(c *gin.Context) {
	session := sessions.Default(c)
	session.Delete("authenticated")
	session.Save()
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "已退出登录"})
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		if auth, ok := session.Get("authenticated").(bool); ok && auth {
			c.Next()
			return
		}
		if c.GetHeader("Accept") == "application/json" || c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "msg": "未登录"})
		} else {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
		}
	}
}

// ==================== 配置 API ====================

func getConfigHandler(c *gin.Context) {
	cfg := core.GetConfig()
	c.JSON(http.StatusOK, gin.H{
		"bot_token":            cfg.TelegramBot.Token,
		"user_id":              cfg.TelegramBot.AllowedUser,
		"notification_enabled": cfg.Notification.Enabled,
		"tg_channels":          cfg.TgChannels,
		// ===== 新增：返回用户名给前端显示 =====
		"system": gin.H{
			"username": cfg.System.Username,
		},
	})
}

func updateConfigHandler(c *gin.Context) {
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "msg": "无效JSON"})
		return
	}
	if err := core.UpdateConfig(updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "msg": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

// ==================== 通知渠道 CRUD ====================

func createChannelHandler(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
		APIURL   string `json:"api_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效请求"})
		return
	}
	if req.Name == "" || req.BotToken == "" || req.ChatID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "请填写完整信息"})
		return
	}
	id := core.GenID()
	if err := core.AddChannel(id, req.Name, req.BotToken, req.ChatID, req.APIURL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "创建成功", "id": id})
}

func updateChannelHandler(c *gin.Context) {
	var req struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
		APIURL   string `json:"api_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效请求"})
		return
	}
	if err := core.UpdateChannel(req.ID, req.Name, req.BotToken, req.ChatID, req.APIURL); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "更新成功"})
}

func deleteChannelHandler(c *gin.Context) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效请求"})
		return
	}
	if err := core.DeleteChannel(req.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "删除成功"})
}
func testChannelHandler(c *gin.Context) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效请求"})
		return
	}
	if req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "渠道 ID 不能为空"})
		return
	}
	if err := core.TestTelegramChannel(req.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "测试消息发送成功"})
}
func sehuaListHandler(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	section, _ := strconv.Atoi(c.DefaultQuery("section", "103"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 150 {
		limit = 30
	}
	offset := (page - 1) * limit

	dbPath := "/app/data/data.db"
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
		return
	}
	defer db.Close()

	var tableExists int
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sehua_data'").Scan(&tableExists)
	if err != nil || tableExists == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
		return
	}

	var colExists int
	err = db.QueryRow("SELECT count(*) FROM pragma_table_info('sehua_data') WHERE name='section_id'").Scan(&colExists)
	if err != nil {
		colExists = 0
	}

	var total int
	if colExists == 0 {
		sectionName := getSectionName(section)
		err = db.QueryRow(`SELECT COUNT(*) FROM sehua_data WHERE section_name = ?`, sectionName).Scan(&total)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
			return
		}
		rows, err := db.Query(`
			SELECT id, title, movie_name, size, movie_type, post_url, image_path, magnet, publish_date, tags, actress
			FROM sehua_data
			WHERE section_name = ?
			ORDER BY publish_date DESC
			LIMIT ? OFFSET ?`, sectionName, limit, offset)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
			return
		}
		defer rows.Close()
		items := scanRows(rows)
		c.JSON(http.StatusOK, gin.H{
			"ok":    true,
			"data":  items,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	} else {
		err = db.QueryRow(`SELECT COUNT(*) FROM sehua_data WHERE section_id = ?`, section).Scan(&total)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
			return
		}
		rows, err := db.Query(`
			SELECT id, title, movie_name, size, movie_type, post_url, image_path, magnet, publish_date, tags, actress
			FROM sehua_data
			WHERE section_id = ?
			ORDER BY publish_date DESC
			LIMIT ? OFFSET ?`, section, limit, offset)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": []interface{}{}, "total": 0, "page": page, "limit": limit})
			return
		}
		defer rows.Close()
		items := scanRows(rows)
		c.JSON(http.StatusOK, gin.H{
			"ok":    true,
			"data":  items,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	}
}

func scanRows(rows *sql.Rows) []map[string]interface{} {
	items := []map[string]interface{}{}
	for rows.Next() {
		var id int
		var title, movieName, size, movieType, postURL, imagePath, magnet, publishDate, tags, actress sql.NullString
		rows.Scan(&id, &title, &movieName, &size, &movieType, &postURL, &imagePath, &magnet, &publishDate, &tags, &actress)
		items = append(items, map[string]interface{}{
			"id":           id,
			"title":        title.String,
			"movie_name":   movieName.String,
			"size":         size.String,
			"movie_type":   movieType.String,
			"post_url":     postURL.String,
			"image_path":   imagePath.String,
			"magnet":       magnet.String,
			"publish_date": publishDate.String,
			"tags":         tags.String,
			"actress":      actress.String,
		})
	}
	return items
}

func getSectionName(sectionID int) string {
	switch sectionID {
	case 2:
		return "国产原创"
	case 36:
		return "亚洲无码原创"
	case 151:
		return "4K原版"
	default:
		return "高清中文字幕"
	}
}

func sehuaFetchHandler(c *gin.Context) {
	var req struct {
		Page    int `json:"page"`
		Section int `json:"section"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Page < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效页码"})
		return
	}
	if req.Section == 0 {
		req.Section = 103
	}
	if err := core.RunSehuaSpiderPage(req.Page, req.Section, false); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "爬取任务已启动，请稍后刷新"})
}

func sehuaImageHandler(c *gin.Context) {
	filename := c.Param("filename")
	if filename == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	imageDir := "./data/sehua"
	cleanPath := filepath.Clean(filepath.Join(imageDir, filename))
	if !strings.HasPrefix(cleanPath, filepath.Clean(imageDir)) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.File(cleanPath)
}

func sehuaDownloadHandler(c *gin.Context) {
	var req struct {
		Magnet    string `json:"magnet"`
		AccountID string `json:"account_id"`
		SavePath  string `json:"save_path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "无效请求"})
		return
	}
	if req.Magnet == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "磁力链接不能为空"})
		return
	}

	var cookie string
	if req.AccountID != "" {
		acc := core.GetAccountByID(req.AccountID)
		if acc == nil {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "msg": "账号不存在"})
			return
		}
		cookie = acc.Cookie
	} else {
		accounts := core.GetAccounts()
		if len(accounts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": "请先添加115账号"})
			return
		}
		cookie = accounts[0].Cookie
	}

	if err := core.AddOfflineTask(cookie, req.Magnet, req.SavePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "离线任务已添加"})
}

func sehuaCrawlHandler(c *gin.Context) {
	var req struct {
		Date    string `json:"date"`
		Section int    `json:"section"`
	}
	c.ShouldBindJSON(&req)
	if req.Section == 0 {
		req.Section = 103
	}
	if err := core.RunSehuaSpider(req.Date, req.Section, false); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "爬虫任务已启动"})
}

func sehuaStatusHandler(c *gin.Context) {
	status := core.GetSpiderStatus()
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": status})
}